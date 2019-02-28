package main

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/GodlikePenguin/agogos-datatypes"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var applicationList apps
var hostMap concurrentMap
var hostCheck string

type apps struct {
	sync.Mutex
	applications []Datatypes.Application
}

func (a *apps) getApps() []Datatypes.Application {
	a.Lock()
	toReturn := a.applications
	a.Unlock()
	return toReturn
}

func (a *apps) setApps(app []Datatypes.Application) {
	a.Lock()
	a.applications = app
	a.Unlock()
}

type concurrentMap struct {
	sync.Mutex
	hosts map[string]string
}

func (c *concurrentMap) getHosts() map[string]string {
	c.Lock()
	hosts := c.hosts
	c.Unlock()
	return hosts
}

func (c *concurrentMap) setHosts(newHosts map[string]string) {
	c.Lock()
	c.hosts = newHosts
	c.Unlock()
}



func handleTunneling(w http.ResponseWriter, req *http.Request) {
	_, err := net.LookupIP(req.Host)
	if err != nil {
		//Try to find the container which made the request
		appCopy := applicationList.getApps()

		app, err := getMatchingApplication(appCopy, req.RemoteAddr)
		if err != nil {
			log.Println(err)
			log.Println("Can't find what application this request came from. Let's just give up and report the HTTP error")

		} else {
			//try to rewrite the URL with a local match
			if !checkForLocalMatch(req, app, appCopy) {
				//If that fails, check for a remote match
				checkForRemoteMatch(req, app, appCopy)
			}
		}
	}
	dest_conn, err := net.DialTimeout("tcp", req.Host, 10*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Add("proxy-chosen-ip", req.URL.Hostname())
	w.WriteHeader(http.StatusOK)
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
		return
	}
	client_conn, _, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	}
	go transfer(dest_conn, client_conn)
	go transfer(client_conn, dest_conn)
}

func transfer(destination io.WriteCloser, source io.ReadCloser) {
	defer destination.Close()
	defer source.Close()
	io.Copy(destination, source)
}

func handleHTTP(w http.ResponseWriter, req *http.Request) {
	_, err := net.LookupIP(req.Host)
	if err != nil {
		//Try to find the container which made the request
		appCopy := applicationList.getApps()

		app, err := getMatchingApplication(appCopy, req.RemoteAddr)
		if err != nil {
			log.Println(err)
			log.Println("Can't find what application this request came from. Let's just give up and report the HTTP error")

		} else {
			//try to rewrite the URL with a local match
			if !checkForLocalMatch(req, app, appCopy) {
				//If that fails, check for a remote match
				checkForRemoteMatch(req, app, appCopy)
			}
		}
	}
	log.Println("Using request URL:", req.URL.String())
	//Try to execute to query, no matter what the final request is
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()
	copyHeader(w.Header(), resp.Header)
	w.Header().Add("proxy-chosen-ip", req.URL.Hostname())
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func main() {
	host := os.Getenv("AGOGOS_HOSTNAME")
	if host == "" {
		panic("AGOGOS_HOSTNAME not set. Exiting")
	}
	hostCheck = host
	server := &http.Server{
		Addr: ":8888",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodConnect {
				handleTunneling(w, r)
			} else {
				handleHTTP(w, r)
			}
		}),
		// Disable HTTP/2.
		TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
	}
	log.Println("Starting app watcher")
	go appWatcher()
	log.Println("Starting proxy")
	log.Fatal(server.ListenAndServe())
}

func appWatcher() {
	for {
		time.Sleep(5 * time.Second)
		resp, err := getRequest("http://host.docker.internal:14440/applications")
		if err != nil {
			log.Println("Error fetching applications: ", err.Error())
			continue
		}

		var a []Datatypes.Application
		if err = json.Unmarshal(resp, &a); err != nil {
			log.Println("Error unmarshalling json response: ", err.Error())
			continue
		}
		applicationList.setApps(a)

		resp, err = getRequest("http://host.docker.internal:14440/nodes")
		if err != nil {
			log.Println("Error fetching nodes: ", err.Error())
			continue
		}

		var n []Datatypes.Node
		if err = json.Unmarshal(resp, &n); err != nil {
			log.Println("Error unmarshalling json response: ", err.Error())
			continue
		}
		hMap := make(map[string]string)
		for _, no := range n {
			if no.Active {
				hMap[no.Name] = no.Address
			}
		}
		hostMap.setHosts(hMap)
	}
}

func getRequest(address string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Add("X-agogos-disable-log", "true")

	client := http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(strconv.Itoa(resp.StatusCode) + " " + string(body))
	}

	return body, nil
}

func checkForLocalMatch(r *http.Request, app Datatypes.Application, allApps []Datatypes.Application) bool {
	IPs := findValidIPs(app, allApps, r.URL.Hostname())
	if len(IPs) == 0 {
		log.Println("No valid local IPs for ", r.URL.Hostname())
		return false
	}
	var validIPs []string
	for _, IP := range IPs {
		timeout := time.Duration(500 * time.Millisecond)
		port := r.URL.Port()
		if port == "" {
			port = "80"
		}
		_, err := net.DialTimeout("tcp",fmt.Sprintf("%s:%s", IP, port), timeout)
		if err != nil {
			if strings.Contains(err.Error(), "i/o timeout") {
				//If it's a timeout, we ignore this IP and assume it's gone down, and try to pick another one
				//we will allow all other errors as they could be requesting the wrong port etc
				continue
			}
		}
		validIPs = append(validIPs, IP)
	}

	if len(validIPs) == 0 {
		//If no IPs are valid then who cares, just pick one and give an error
		validIPs = IPs
	}

	randomIP := validIPs[rand.Intn(len(validIPs))]
	println("picked IP: ", randomIP, " for ", r.URL.Hostname())
	newURL, err := url.Parse(strings.Replace(r.URL.String(), r.URL.Hostname(), randomIP, 1))
	if err != nil {
		log.Println(err)
		log.Println("Could not form new URL for proxied request")
		return false
	}
	r.URL = newURL
	r.Host = r.URL.Host
	return true
}

func getMatchingApplication(apps []Datatypes.Application, address string) (Datatypes.Application, error) {
	split := strings.Split(address, ":")
	if len(split) != 2 {
		return Datatypes.Application{}, errors.New("address should be in the form IP:PORT")
	}
	for _, app := range apps {
		for _, comp := range app.Components {
			for _, netw := range comp.NetworkInfo {
				for _, v := range netw {
					if v == split[0] {
						if app.Node == hostCheck {
							return app, nil
						}
					}
				}
			}
		}
	}
	return Datatypes.Application{}, errors.New("could not find app")
}

func findValidIPs(application Datatypes.Application, apps []Datatypes.Application, hostname string) []string {
	//Check the implicit networks
	var ret []string
	for _, comp := range application.Components {
		if comp.Name == hostname {
			//This proxy has to be able to access the network so we pick the Bridge IP
			ret = append(ret, comp.NetworkInfo["bridge"]...)
		}
	}

	//Check for apps on the same node in the same network
	for _, app := range apps {
		if app.Name == application.Name {
			continue
		}
		if app.Node != application.Node {
			continue
		}
		if overlap(application.Networks, app.Networks) {
			for _, comp := range app.Components {
				if comp.Name == hostname {
					ret = append(ret, comp.NetworkInfo["bridge"]...)
				}
			}
		}
	}
	return ret
}

func overlap(a, b []string) bool {
	for _, i := range a {
		if i == "bridge" {
			continue
		}
		for _, j := range b {
			if i == j {
				return true
			}
		}
	}
	for _, i := range b {
		if i == "bridge" {
			continue
		}
		for _, j := range a {
			if i == j {
				return true
			}
		}
	}
	return false
}

func checkForRemoteMatch(r *http.Request, app Datatypes.Application, allApps []Datatypes.Application) {
	remoteApps := findValidRemoteApps(app, allApps, r.URL.Hostname())
	if len(remoteApps) == 0 {
		log.Println("No valid remoteApps for ", r.URL.Hostname())
		return
	}

	var validApps []Datatypes.Application
	for _, appl := range remoteApps {
		timeout := time.Duration(500 * time.Millisecond)
		//Checking for a remote proxy here so don't honor the host
		port := "14442"
		_, err := net.DialTimeout("tcp",fmt.Sprintf("%s:%s", ipForHost(appl.Node), port), timeout)
		if err != nil {
			if strings.Contains(err.Error(), "i/o timeout") {
				//If it's a timeout, we ignore this host and assume it's gone down, and try to pick another one
				//we will allow all other errors as they could be requesting the wrong port etc
				continue
			}
		}
		validApps = append(validApps, appl)
	}

	if len(validApps) == 0 {
		//If no remoteApps are valid then who cares, just pick one and give an error
		validApps = remoteApps
	}

	randomApp := validApps[rand.Intn(len(validApps))]
	println("picked node: ", randomApp.Node, " for ", r.URL.Hostname())

	oldPort := r.URL.Port()
	if oldPort != "" {
	} else {
		oldPort = "80"
	}
	requestedHostname := r.URL.Hostname()
	origRequest := r.URL.String()
	newURL, err := url.Parse(fmt.Sprintf("http://%s:%s/%s/%s", ipForHost(randomApp.Node), "14442", randomApp.Name, requestedHostname))
	if err != nil {
		log.Println(err)
		log.Println("Could not form new URL for proxied request")
		return
	}
	r.Header.Add("X-agogos-requested-port", oldPort)
	r.Header.Add("X-agogos-query", origRequest)
	r.URL = newURL
	r.Host = r.URL.Host
}

func findValidRemoteApps(application Datatypes.Application, apps []Datatypes.Application, hostname string) []Datatypes.Application {
	//Check for apps on a different node in the same network
	var ret []Datatypes.Application
	for _, app := range apps {
		if app.Name == application.Name {
			continue
		}
		if app.Node == application.Node {
			continue
		}
		if overlap(application.Networks, app.Networks) {
			for _, comp := range app.Components {
				if comp.Name == hostname {
					ret = append(ret, app)
				}
			}
		}
	}
	return ret
}

func ipForHost(hostname string) string {
	return hostMap.getHosts()[hostname]
}
