package main

import (
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"golang.org/x/net/context"
	"os"
	"strings"
	"flag"
	//"io"
)

const CONF_DIR = "./config"
const HOSTNAME = "localhost"

var proxy_id *string
var cli *client.Client
var hostname *string

func shortID(id string) string {
	return id[0:12]
}

func signalProxy() {
	if proxy_id == nil {
		fmt.Fprintln(os.Stderr, "No proxy-container detected (label function=auto.proxy), not sending reload signal (SIGHUP)")
		return
	}
	err := cli.ContainerKill(context.Background(), *proxy_id, "HUP")
	if err == nil {
		fmt.Printf("Signaled proxy-container %s to reload (SIGHUP)\n", shortID(*proxy_id))
	} else {
		fmt.Fprintf(os.Stderr, "Unable to signel proxy-container %s: %s\n", shortID(*proxy_id), err)
	}
}

func configPath(containerId string) string {
	return fmt.Sprintf("%s/_%s.conf", CONF_DIR, shortID(containerId))
}

func removeContainer(containerId string) {
	if proxy_id != nil && containerId == *proxy_id {
		// proxy stopped, remove reference
		proxy_id = nil
		return
	}

	// look for a config-file to remove
	_, err := os.Stat(configPath(containerId))
	if err == nil {
		fmt.Printf("Removed config-file: %s\n", configPath(containerId))
		os.Remove(configPath(containerId))
		signalProxy()
	}
	
}

func checkContainer(containerId string) {
	fmt.Println("checking container:", shortID(containerId))
	cInfo, err := cli.ContainerInspect(context.Background(), containerId)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Unable to inspect container:", err)
		return
	}

	// look for public containers
	containerType, ok := cInfo.Config.Labels["function"]
	if !ok {
		fmt.Fprintln(os.Stderr, "Error, unable to read container labels for container:", shortID(containerId))
	}

	switch containerType {
	case "web":
		// generate web-config
		updateSiteConfig(containerId, cInfo)
	case "auto.proxy":
		// point proxy-server to this container
		fmt.Println("Updating proxy-container to:", shortID(containerId))
		proxy_id = &containerId
	default:
		fmt.Fprintf(os.Stderr, "Error, unknown container-label type %s on container: %s\n", containerType, shortID(containerId))
	}
}

func updateSiteConfig(containerId string, cInfo types.ContainerJSON) {
	// look for public containers
	name := strings.TrimPrefix(cInfo.Name, "/")
	fmt.Println("Updating config for web-container:", name)

	// hostnames to server
	server_names := make([]string, 0)
	var first_alias string


	for _, net := range cInfo.NetworkSettings.Networks {
		for _, alias := range net.Aliases {
			if first_alias == "" {
				first_alias = alias
			}
			server_names = append(server_names, fmt.Sprintf("%s.%s", alias, *hostname))
		}
	}

	// look for hostnames
	extra_names, ok := cInfo.Config.Labels["hostname"]
	if (ok) {
		hostnames := strings.Split(extra_names, ",")
		if len(hostnames) > 0 {
			fmt.Println("Found additional hostnames:", extra_names)
			server_names = append(server_names, hostnames...)
		}
	}

	f, err := os.Create(configPath(containerId))
	if err != nil {
		fmt.Fprintln(os.Stderr, "Unable to write file to CONF_DIR:", err)
		return
	}
	defer f.Close()
	f.WriteString("server {\n")
	f.WriteString("  listen 80;\n")
	f.WriteString(fmt.Sprintf("  server_name %s;\n", strings.Join(server_names, " ")))
	f.WriteString(fmt.Sprintf("  proxy_pass http://%s:80;\n", first_alias))
	//f.WriteString(fmt.Sprintf("  proxy_pass http://%s:80;\n", cInfo.NetworkSettings.DefaultNetworkSettings.IPAddress))
	f.WriteString("}\n")
	f.Sync()

	signalProxy()
}

func deleteAllConfigs() {
	d, err := os.Open(CONF_DIR)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Unable to open CONF_DIR")
		return
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Unable to read CONF_DIR")
		return
	}
	for _, name := range names {

		// only delete files that start with "_"
		if !strings.HasPrefix(name, "_") {
			continue
		}
		os.Remove(fmt.Sprintf("%s/%s", CONF_DIR, name))
	} 

}

func main() {
	os.Setenv("DOCKER_API_VERSION", "1.35")

	// default hostname for services
	hostname = flag.String("domain", "localhost", "Domain-name to add to preview-links")

	flag.Parse()

	fmt.Println("My hostname:", *hostname)

	var err error
	cli, err = client.NewEnvClient()
	if err != nil {
		panic(err)
	}

	// only receive containers we are interested in
	run_filter := filters.NewArgs()
	run_filter.Add("label", "function")

	// Available filters here: https://github.com/docker/cli/blob/master/docs/reference/commandline/ps.md
	containers, err := cli.ContainerList(context.Background(), types.ContainerListOptions{Filters: run_filter})
	if err != nil {
		panic(err)
	}

	ev_filter := filters.NewArgs()
	ev_filter.Add("type", "container")
	ev_filter.Add("event", "start")
	ev_filter.Add("event", "die")
	ev_filter.Add("label", "function")



	// delete existing config_files
	deleteAllConfigs()

	for _, container := range containers {
		if container.State == "running" {
			checkContainer(container.ID)
		}
	}

	// ev and err are channels!
	ev_ch, err_ch := cli.Events(context.Background(), types.EventsOptions{Filters: ev_filter})
	for {
		select {
		case err = <- err_ch:
				panic(err)
		case ev := <- ev_ch:
			if ev.Action == "start" {
				checkContainer(ev.ID)
			} else {
				removeContainer(ev.ID)
				fmt.Println("Stopped Container:", shortID(ev.ID))
			}
		}
	}
}