package main

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"github.com/GodlikePenguin/agogos-datatypes"
	"github.com/davecgh/go-spew/spew"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

var applicationList apps

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



func handleTunneling(w http.ResponseWriter, r *http.Request) {
	checkForLocalMatch(r)
	dest_conn, err := net.DialTimeout("tcp", r.Host, 10*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
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
	checkForLocalMatch(req)
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()
	copyHeader(w.Header(), resp.Header)
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
	}
}

func getRequest(address string) ([]byte, error) {
	resp, err := http.Get(address)
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

func checkForLocalMatch(r *http.Request) {
	appCopy := applicationList.getApps()
	_, err := net.LookupIP(r.Host)
	if err == nil {
		//we're not needed here
		return
	}

	app, err := getMatchingApplication(appCopy, r.RemoteAddr)
	if err != nil {
		log.Println(err)
		log.Println("Can't find what application this request came from. Let's just give up and report the HTTP error")
		spew.Dump(appCopy)
		spew.Dump(r.RemoteAddr)
		return
	}
	IPs := findValidIPs(app, appCopy, r.Host)
	if len(IPs) == 0 {
		log.Println("No valid IPs for ", r.Host)
		return
	}
	println("Found local IPs: ", IPs, " for ", r.Host)
	randomIP := rand.Intn(len(IPs))
	r.RequestURI = strings.Replace(r.RequestURI, r.Host, IPs[randomIP], 1)
	r.Host = IPs[randomIP]
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
						return app, nil
					}
				}
			}
		}
	}
	return Datatypes.Application{}, errors.New("could not find app")
}

func findValidIPs(application Datatypes.Application, apps []Datatypes.Application, hostname string) []string {
	for _, comp := range application.Components {
		if comp.Name == hostname {
			var ret []string
			for _, v := range comp.NetworkInfo {
				ret = append(ret, v...)
			}
			return ret
		}
	}
	//TODO local applications outside of the implicit network
	return []string{}
}
