package main

import (
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"golang.org/x/net/context"
	"os"
	"strings"
	"regexp"
	"flag"
	"log"
	//"io"
)

type certPathData struct {
  dir string
  cert string
  key string
}

var proxy_id *string
var cli *client.Client
var hostname string
var confDir string
var sslDir string

func shortID(id string) string {
	return id[0:12]
}

func signalProxy() {
	if proxy_id == nil {
		log.Printf("No proxy-container detected (label function=auto.proxy), not sending reload signal (SIGHUP)")
		return
	}
	err := cli.ContainerKill(context.Background(), *proxy_id, "HUP")
	if err == nil {
		log.Printf("Signaled proxy-container %s to reload (SIGHUP)", shortID(*proxy_id))
	} else {
		log.Printf("Unable to signel proxy-container %s: %s", shortID(*proxy_id), err)
	}
}

func configPath(containerId string) string {
	return fmt.Sprintf("%s/_%s.conf", confDir, shortID(containerId))
}

func configPathTLS(containerId string) string {
	return fmt.Sprintf("%s/_%s-ssl.conf", confDir, shortID(containerId))
}

func certPath(hostname string) certPathData {
	return certPathData{
		dir: fmt.Sprintf("%s/%s", sslDir, hostname),
		cert: "fullchain.pem",
		key: "privkey.pem",
	}
}

func removeContainer(containerId string) {
	if proxy_id != nil && containerId == *proxy_id {
		// proxy stopped, remove reference
		log.Printf("proxy-container %s stopped, removing reference", shortID(containerId))
		proxy_id = nil
		return
	}

	// look for a config-file to remove
	if fileExists(configPath(containerId)) {
		log.Printf("Removing config-file: %s", configPath(containerId))
		os.Remove(configPath(containerId))
		signalProxy()
	}
	
}

func checkContainer(containerId string) bool {
	cInfo, err := cli.ContainerInspect(context.Background(), containerId)
	if err != nil {
		log.Fatalln("Unable to inspect container:", err)
	}

	// look for public containers
	containerType, ok := cInfo.Config.Labels["function"]
	if !ok {
		log.Fatalln("Error, unable to read container labels for container:", shortID(containerId))
	}

	switch containerType {
	case "web":
		// generate web-config
		updateSiteConfig(containerId, cInfo)
		return true
	case "auto.proxy":
		// point proxy-server to this container
		log.Println("Updating proxy-container to container ID:", shortID(containerId))
		proxy_id = &containerId
	default:
		log.Printf("Error, unknown container-label type %s on container: %s", containerType, shortID(containerId))
	}

	// no need to signal proxy
	return false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func findCertificate(server_names []string) *certPathData {
	if len(server_names) > 0 {
		// use first matching cert
		for _, n := range server_names {
			p := certPath(n)
			if fileExists(p.dir) {
				return &p
			}
		}
	}
	// nothing found
	return nil
}

func updateSiteConfig(containerId string, cInfo types.ContainerJSON) {
	// look for public containers
	name := strings.TrimPrefix(cInfo.Name, "/")
	log.Printf("<--")
	log.Printf("Updating config for web-container %s (ID: %s)", name, shortID(containerId))
	// grab hostname

	// hostnames to server
	server_names := make([]string, 0)
	var first_alias string


	for _, net := range cInfo.NetworkSettings.Networks {
		for _, alias := range net.Aliases {
			if first_alias == "" {
				first_alias = alias
			}
			break
		}
	}

	// look for hostnames
	extra_names, ok := cInfo.Config.Labels["hostname"]
	if (ok) {
		hostnames := strings.Split(extra_names, ",")
		if len(hostnames) > 0 {
			server_names = append(server_names, hostnames...)
		}
	}

	// append a temporary name if its valid..
	if ok, _ = regexp.MatchString("^[a-zA-Z0-9]+$", name); ok {
		server_names = append(server_names, fmt.Sprintf("%s.%s", name, hostname))
	}

	// setup tls if there is any matching certificate
	cert := findCertificate(server_names)
	if cert != nil {
		writeConfig(configPath(containerId), true, cert, server_names, first_alias)
	} else {
		writeConfig(configPath(containerId), false, nil, server_names, first_alias)
	}
	log.Printf("-->")
}

func writeConfig(path string, ssl bool, cert *certPathData, server_names []string, alias string) {
	f, err := os.Create(path)
	if err != nil {
		log.Fatalf("Unable to create file %s: %s", path, err)
	}
	f.WriteString("server {\n")
	if ssl && cert != nil{
		f.WriteString("  listen 443 ssl http2;\n")
		f.WriteString(fmt.Sprintf("  ssl_certificate %s/%s;\n", cert.dir, cert.cert))
		f.WriteString(fmt.Sprintf("  ssl_certificate_key %s/%s;\n\n", cert.dir, cert.key))
		f.WriteString("if ($scheme=='http') { return 302 http://$server_name$request_uri; }\n")
	}
	f.WriteString("  listen 80;\n")
	f.WriteString(fmt.Sprintf("  server_name %s %s.%s;\n", strings.Join(server_names, " "), alias, hostname))
	f.WriteString("  location / {\n")
	f.WriteString("    include proxy.conf;\n")
	f.WriteString(fmt.Sprintf("    proxy_pass http://%s:80;\n", alias))
	f.WriteString("  }\n")
	f.WriteString("}\n")

	log.Printf("server_names %s %s.%s (ssl: %t)", strings.Join(server_names, " "), alias, hostname, ssl && cert != nil)

	f.Sync()
	f.Close()
}

func deleteAllConfigs() {
	d, err := os.Open(confDir)
	if err != nil {
		log.Fatalf("Unable to open config dir '%s': %s", confDir, err)
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		log.Fatalf("Unable to read from config dir '%s': %s", confDir, err)
	}
	for _, name := range names {

		// only delete files that start with "_"
		if !strings.HasPrefix(name, "_") {
			continue
		}
		os.Remove(fmt.Sprintf("%s/%s", confDir, name))
	} 

}

func main() {
	os.Setenv("DOCKER_API_VERSION", "1.35")

	// default hostname for services
	flag.StringVar(&hostname, "domain", "localhost", "Domain-name to add to preview-links")
	flag.StringVar(&confDir, "confDir", "/config", "Directory to write generated config-files")
	flag.StringVar(&sslDir, "sslDir", "/ssl", "Directory to look for certificates")



	flag.Parse()

	// directory to write config
	log.Print("Starting..")
	log.Printf("Hostname to add to preview-links: %s", hostname)
	log.Printf("Directory to write config: %s", confDir)
	log.Printf("Directory to look for TLS-certificates: %s", sslDir)


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

	log.Print("Looking for running containers with known functions (label: function=*)")

	reload := false

	for _, container := range containers {
		if container.State == "running" {
			if checkContainer(container.ID) {
				reload = true
			}
		}
	}

	// signal proxy to refresh
	if reload {
		signalProxy()
	}

	// ev and err are channels!
	ev_ch, err_ch := cli.Events(context.Background(), types.EventsOptions{Filters: ev_filter})
	for {
		select {
		case err = <- err_ch:
				panic(err)
		case ev := <- ev_ch:
			if ev.Action == "start" {
				if checkContainer(ev.ID) {
					signalProxy()
				}
			} else {
				removeContainer(ev.ID)
			}
		}
	}
}