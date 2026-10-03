package docker

import (
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
)

func TestConfigureProcessLeavesTheImageAloneUnlessOverridden(t *testing.T) {
	cfg := &container.Config{Image: "app:1"}
	configureProcess(ContainerSpec{}, cfg)
	if cfg.Entrypoint != nil || cfg.Cmd != nil || cfg.User != "" {
		t.Errorf("an unset override must stay unset, so the image's own applies: %+v", cfg)
	}

	configureProcess(ContainerSpec{
		Entrypoint: []string{"dotnet"},
		Command:    []string{"App.dll", "--urls", "http://0.0.0.0:8080"},
		User:       "1000:1000",
	}, cfg)
	if strings.Join(cfg.Entrypoint, " ") != "dotnet" || strings.Join(cfg.Cmd, " ") != "App.dll --urls http://0.0.0.0:8080" || cfg.User != "1000:1000" {
		t.Errorf("overrides not applied: %+v", cfg)
	}
}

func TestConfigureLogging(t *testing.T) {
	defaults := func() *container.HostConfig {
		return &container.HostConfig{LogConfig: container.LogConfig{
			Type:   "json-file",
			Config: map[string]string{"max-size": "10m", "max-file": "3"},
		}}
	}

	t.Run("no driver keeps the default", func(t *testing.T) {
		host := defaults()
		configureLogging(ContainerSpec{}, host)
		if host.LogConfig.Type != "json-file" || host.LogConfig.Config["max-size"] != "10m" {
			t.Errorf("LogConfig = %+v", host.LogConfig)
		}
	})

	t.Run("a remote driver gets exactly its options", func(t *testing.T) {
		host := defaults()
		configureLogging(ContainerSpec{LogDriver: "gelf", LogOptions: map[string]string{"gelf-address": "udp://logs.example.com:12201"}}, host)
		if host.LogConfig.Type != "gelf" || len(host.LogConfig.Config) != 1 || host.LogConfig.Config["gelf-address"] != "udp://logs.example.com:12201" {
			t.Errorf("LogConfig = %+v; the disk caps mean nothing to a collector", host.LogConfig)
		}
	})

	t.Run("the local drivers keep the caps", func(t *testing.T) {
		for _, driver := range []string{"json-file", "local"} {
			host := defaults()
			opts := map[string]string{"compress": "true"}
			configureLogging(ContainerSpec{LogDriver: driver, LogOptions: opts}, host)
			got := host.LogConfig.Config
			if host.LogConfig.Type != driver || got["max-size"] != "10m" || got["max-file"] != "3" || got["compress"] != "true" {
				t.Errorf("%s: LogConfig = %+v", driver, host.LogConfig)
			}
			if _, leaked := opts["max-size"]; leaked {
				t.Errorf("%s: the caps were written into the application's own options", driver)
			}
		}
	})

	t.Run("caps set in deploy.yaml win", func(t *testing.T) {
		host := defaults()
		configureLogging(ContainerSpec{LogDriver: "local", LogOptions: map[string]string{"max-size": "50m"}}, host)
		if got := host.LogConfig.Config; got["max-size"] != "50m" || got["max-file"] != "3" {
			t.Errorf("LogConfig = %+v", host.LogConfig)
		}
	})
}

func TestConfigureInitAsksForAnInitProcessOnlyOnRequest(t *testing.T) {
	host := &container.HostConfig{}
	configureInit(ContainerSpec{}, host)
	if host.Init != nil {
		t.Errorf("Init = %v without `init`; unset leaves the daemon's own default alone", *host.Init)
	}
	configureInit(ContainerSpec{Init: true}, host)
	if host.Init == nil || !*host.Init {
		t.Error("`init: true` did not reach the container's configuration")
	}
}
