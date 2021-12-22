package head

import (
	"context"
	logging "github.com/ipfs/go-log/v2"
	"io/ioutil"
	"net"
	"net/http"
	"path"
	"sync"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p-core/host"
	peer "github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
	gostream "github.com/libp2p/go-libp2p-gostream"
)

const closeTimeout = 30 * time.Second

var log = logging.Logger("go-legs/head")

type Publisher struct {
	rl     sync.RWMutex
	root   cid.Cid
	server *http.Server
}

func NewPublisher() *Publisher {
	p := &Publisher{
		server: &http.Server{},
	}
	p.server.Handler = http.Handler(p)
	return p
}

func deriveProtocolID(topic string) protocol.ID {
	return protocol.ID("/legs/head/" + topic + "/0.0.1")
}

func (p *Publisher) Serve(host host.Host, topic string) error {
	pid := deriveProtocolID(topic)
	l, err := gostream.Listen(host, pid)
	if err != nil {
		log.Errorf("Failed to listen to gostream on host %s with prpotocol ID %s", host.ID(), pid)
		return err
	}
	log.Infow("Serving gostream", "host", host.ID(), "protocolID", pid)
	return p.server.Serve(l)
}

func QueryRootCid(ctx context.Context, host host.Host, topic string, peer peer.ID) (cid.Cid, error) {
	client := http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return gostream.Dial(ctx, host, peer, deriveProtocolID(topic))
			},
		},
	}

	// The httpclient expects there to be a host here. `.invalid` is a reserved
	// TLD for this purpose. See
	// https://datatracker.ietf.org/doc/html/rfc2606#section-2
	resp, err := client.Get("http://unused.invalid/head")
	if err != nil {
		return cid.Undef, err
	}
	defer resp.Body.Close()

	cidStr, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Errorf("Failed to fully read response body: %s", err)
		return cid.Undef, err
	}
	if len(cidStr) == 0 {
		log.Debug("No head is set; returning cid.Undef")
		return cid.Undef, nil
	}

	cs := string(cidStr)
	decode, err := cid.Decode(cs)
	if err != nil {
		log.Errorf("Failed to decode CID %s: %s", cs, err)
	} else {
		log.Debugf("Sucessfully queried latest head %s", decode)
	}
	return decode, err
}

func (p *Publisher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	base := path.Base(r.URL.Path)
	if base != "head" {
		log.Debug("Only head is supported; rejecting reqpuest with base path: %s", base)
		http.Error(w, "", http.StatusNotFound)
		return
	}

	p.rl.RLock()
	defer p.rl.RUnlock()
	var out []byte
	if p.root != cid.Undef {
		currentHead := p.root.String()
		log.Debug("Found current head: %s", currentHead)
		out = []byte(currentHead)
	} else {
		log.Debug("No head is set; responding with empty")
	}

	_, err := w.Write(out)
	if err != nil {
		log.Errorf("Failed to write response: %s", err)
	}
}

func (p *Publisher) UpdateRoot(_ context.Context, c cid.Cid) error {
	p.rl.Lock()
	defer p.rl.Unlock()
	p.root = c
	return nil
}

func (p *Publisher) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	return p.server.Shutdown(ctx)
}
