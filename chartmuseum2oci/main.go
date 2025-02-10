package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"time"

	"github.com/goharbor/go-client/pkg/harbor"
	assistClient "github.com/goharbor/go-client/pkg/sdk/assist/client"
	"github.com/goharbor/go-client/pkg/sdk/v2.0/client"
	"github.com/goharbor/go-client/pkg/sdk/v2.0/client/project"
	"github.com/pkg/errors"
	"github.com/schollz/progressbar/v3"
)

type HelmChart struct {
	Name    string
	Project string
	Version string
}

func (hc HelmChart) ChartFileName() string {
	return fmt.Sprintf("%s-%s.tgz", hc.Name, hc.Version)
}

type ProjectsToMigrateList []string

const (
	fileMode        = 0o600
	helmBinaryPath  = "helm"
	timeout         = 5 * time.Second
	defaultPageSize = 10
)

var (
	sourceHarborURL      string
	sourceHarborUsername string
	sourceHarborPassword string
	destinationHarborURL string
	destinationHarborUsername string
	destinationHarborPassword string
	destPath          string
	projectsToMigrate ProjectsToMigrateList
)

func init() {
	initFlags()
}

func initFlags() {
	flag.StringVar(&sourceHarborURL, "source-url", "", "Source Harbor registry URL")
	flag.StringVar(&sourceHarborUsername, "source-username", "", "Source Harbor registry username")
	flag.StringVar(&sourceHarborPassword, "source-password", "", "Source Harbor registry password")
	flag.StringVar(&destinationHarborURL, "destination-url", "", "Destination Harbor registry URL")
	flag.StringVar(&destinationHarborUsername, "destination-username", "", "Destination Harbor registry username")
	flag.StringVar(&destinationHarborPassword, "destination-password", "", "Destination Harbor registry password")
	flag.StringVar(&destPath, "destpath", "", "Destination subpath")
	flag.Var(&projectsToMigrate, "project", "Name of the project(s) to migrate")
	flag.Parse()

	if sourceHarborURL == "" || destinationHarborURL == "" {
		log.Fatal(errors.New("Missing required --source-url or --destination-url flag"))
	}
}

func main() {
	if err := helmLogin(sourceHarborURL, sourceHarborUsername, sourceHarborPassword); err != nil {
		log.Fatal(errors.Wrap(err, "Failed to login to source Harbor"))
	}

	if err := helmLogin(destinationHarborURL, destinationHarborUsername, destinationHarborPassword); err != nil {
		log.Fatal(errors.Wrap(err, "Failed to login to destination Harbor"))
	}

	helmChartsToMigrate, err := getHarborChartmuseumCharts()
	if err != nil {
		log.Fatal(errors.Wrap(err, "Failed to retrieve Helm charts from source"))
	}

	log.Printf("%d Helm charts to migrate", len(helmChartsToMigrate))
	bar := progressbar.Default(int64(len(helmChartsToMigrate)))
	errorCount := 0

	for _, helmChart := range helmChartsToMigrate {
		_ = bar.Add(1)
		if err := migrateChartFromSourceToDestination(helmChart); err != nil {
			errorCount++
			log.Println(errors.Wrap(err, "Failed to migrate Helm chart"))
		}
	}

	log.Printf("%d Helm charts successfully migrated", len(helmChartsToMigrate)-errorCount)
}

func helmLogin(registry, username, password string) error {
	cmd := exec.Command(helmBinaryPath, "registry", "login", "--username", username, "--password", password, registry)
	var stdErr bytes.Buffer
	cmd.Stderr = &stdErr

	if err := cmd.Run(); err != nil {
		return errors.Wrapf(err, "Failed to execute helm login: %s", stdErr.String())
	}
	return nil
}

func migrateChartFromSourceToDestination(helmChart HelmChart) error {
	if err := pullChartFromSource(helmChart); err != nil {
		return errors.Wrap(err, "Failed to pull chart from source")
	}

	if err := pushChartToDestination(helmChart); err != nil {
		return errors.Wrap(err, "Failed to push chart to destination")
	}

	return removeChartFile(helmChart)
}

func pullChartFromSource(helmChart HelmChart) error {
	chartFileName := helmChart.ChartFileName()
	sourceURL := fmt.Sprintf("%s/chartrepo/%s/charts/%s", sourceHarborURL, helmChart.Project, chartFileName)

	req, err := http.NewRequest(http.MethodGet, sourceURL, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(sourceHarborUsername, sourceHarborPassword)

	client := &http.Client{Timeout: timeout}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("received status %d", res.StatusCode)
	}

	resBody, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}

	return os.WriteFile(chartFileName, resBody, fileMode)
}

func pushChartToDestination(helmChart HelmChart) error {
	repoURL := fmt.Sprintf("oci://%s/%s%s", destinationHarborURL, helmChart.Project, destPath)
	cmd := exec.Command(helmBinaryPath, "push", helmChart.ChartFileName(), repoURL)

	var stdErr bytes.Buffer
	cmd.Stderr = &stdErr

	if err := cmd.Run(); err != nil {
		return errors.Wrapf(err, "Failed to execute helm push: %s", stdErr.String())
	}
	return nil
}

func removeChartFile(helmChart HelmChart) error {
	return os.Remove(helmChart.ChartFileName())
}
