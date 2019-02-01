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
	"net"
	"net/http"
	"strconv"
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
	spew.Dump(r)
	log.Println("Called tunnel")
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
	spew.Dump(applicationList.applications)
	spew.Dump(req)
	log.Println("Called HTTP")
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
		resp, err := getRequest("host.docker.internal:14440/applications")
		if err != nil {
			log.Println("Error fetching applications: ", err.Error())
			continue
		}

		var a []Datatypes.Application
		if err = json.Unmarshal(resp, &a); err != nil {
			log.Println("Error unmarshalling json response: ", err.Error())
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
