package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	_ "net/http/pprof"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/whywaita/shoes-lxd-multi/server/pkg/api"
	"github.com/whywaita/shoes-lxd-multi/server/pkg/config"
	"github.com/whywaita/shoes-lxd-multi/server/pkg/lxdclient"
	"github.com/whywaita/shoes-lxd-multi/server/pkg/metric"
)

func main() {
	rand.Seed(time.Now().UnixNano())

	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	hostConfigs, mapping, periodSec, listenPort, overCommitPercent, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to create server: %w", err)
	}

	go serveMetrics(context.Background(), hostConfigs)

	// lxd resource cache
	t := time.NewTicker(time.Duration(periodSec) * time.Second)
	var hcs []config.HostConfig
	hostConfigs.Range(func(key string, value config.HostConfig) bool {
		hcs = append(hcs, value)
		return true
	})
	go setLXDResourceCacheWithTicker(hcs, t)

	server, err := api.New(hostConfigs, mapping, overCommitPercent)
	if err != nil {
		return fmt.Errorf("failed to create server: %w", err)
	}

	if err := server.Run(listenPort); err != nil {
		return fmt.Errorf("faied to run server: %w", err)
	}

	return nil
}

func serveMetrics(ctx context.Context, hostConfigs *config.HostConfigMap) {
	var hcs []config.HostConfig
	hostConfigs.Range(func(key string, value config.HostConfig) bool {
		hcs = append(hcs, value)
		return true
	})

	registry := prometheus.NewRegistry()
	registry.MustRegister(metric.NewCollector(ctx, hcs))
	gatherers := prometheus.Gatherers{
		prometheus.DefaultGatherer,
		registry,
	}

	http.Handle("/metrics", promhttp.HandlerFor(
		gatherers,
		promhttp.HandlerOpts{
			EnableOpenMetrics: true,
		},
	))

	if err := http.ListenAndServe(":9090", nil); err != nil {
		log.Fatal(err)
	}
}

func setLXDResourceCacheWithTicker(hcs []config.HostConfig, ticker *time.Ticker) {
	for {
		_ = <-ticker.C

		log.Print("LXD cache is updating")
		if err := setLXDResourceCache(hcs); err != nil {
			log.Fatal(err)
		}
	}
}

func setLXDResourceCache(hcs []config.HostConfig) error {
	hosts, err := lxdclient.ConnectLXDs(hcs)
	if err != nil {
		return fmt.Errorf("failed to connect LXD hosts: %s", err)
	}

	for _, host := range hosts {
		if err := setLXDHostResourceCache(&host); err != nil {
			return err
		}
	}
	return nil
}

func setLXDHostResourceCache(host *lxdclient.LXDHost) error {
	allCPU, allMemory, hostname, err := lxdclient.ScrapeLXDHostResources(host.Client)
	if err != nil {
		return fmt.Errorf("failed to scrape lxd resources: %s", err)
	}

	instances, err := lxdclient.GetAnyInstances(host.Client)
	if err != nil {
		return fmt.Errorf("failed to retrieve list of instance (host: %s): %s", hostname, err)
	}

	allocatedCPU, allocatedMemory, err := lxdclient.ScrapeLXDHostAllocatedResources(instances)
	if err != nil {
		return fmt.Errorf("failed to scrape instance info: %s", err)
	}

	s := lxdclient.LXDStatus{
		Resource: lxdclient.Resource{
			CPUTotal:    allCPU,
			MemoryTotal: allMemory,
			CPUUsed:     allocatedCPU,
			MemoryUsed:  allocatedMemory,
		},
		HostConfig: host.HostConfig,
	}
	if err := lxdclient.SetStatusCache(host.HostConfig.LxdHost, s); err != nil {
		return fmt.Errorf("failed to set status cache: %s", err)
	}
	return nil
}
