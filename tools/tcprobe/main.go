package main

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/guptarohit/asciigraph"
)

const (
	clientMsg = "ping"
	serverMsg = "pong"
)

//ref madflojo.medium.com/keeping-tcp-connections-alive-in-golang-801a78b7cf1
func server(addr *net.TCPAddr) error {

	// Start TCP Listener
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return fmt.Errorf("Unable to start listener: %v", err)
	}

	// Wait for new connections and send them to reader()
	c, err := l.AcceptTCP()
	if err != nil {
		return fmt.Errorf("Listener returned: %v", err)
	}

	// Enable Keepalives
	err = c.SetKeepAlive(false)
	if err != nil {
		return fmt.Errorf("Unable to set keepalive: %v", err)
	}
	for true {
		msg, err := bufio.NewReader(c).ReadString('\n')
		if err != nil {
			return fmt.Errorf("Unable to read from client: %v", err)
		}
		msg = strings.TrimSuffix(msg, "\n")
		if msg != clientMsg {
			return fmt.Errorf("Received unexpected server message: %s", msg)
		}
		log.Println("received: " + msg)
		log.Println("send: " + serverMsg)
		_, err = fmt.Fprintf(c, fmt.Sprintf("%s\n", serverMsg))
		if err != nil {
			return fmt.Errorf("Unable to send msg: %v", err)
		}
	}
	return nil
}

func client(addr *net.TCPAddr) error {
	graph := NewResponseTimeGraph()
	// Open TCP Connection
	c, err := net.DialTCP("tcp", nil, addr)
	if err != nil {
		return fmt.Errorf("Unable to dial to server: %v", err)
	}

	err = c.SetKeepAlive(false)
	if err != nil {
		return fmt.Errorf("Unable to set keepalive: %v", err)
	}
	for true {
		time.Sleep(1 * time.Second)
		start := time.Now()
		_, err = fmt.Fprintf(c, clientMsg+"\n")
		if err != nil {
			return fmt.Errorf("Unable to send msg: %v", err)
		}
		msg, err := bufio.NewReader(c).ReadString('\n')
		if err != nil {
			return fmt.Errorf("Unable to read from server: %v", err)
		}
		elapsed := time.Since(start)
		msg = strings.TrimSuffix(msg, "\n")
		if msg != serverMsg {
			return fmt.Errorf("Received unexpected server message: %s", msg)
		}
		graph.Plot(elapsed)
	}
	return nil
}

type ResponseTimeGraph struct {
	data                          []float64
	buffer, height, width, offset int
	precision                     uint
	max                           time.Duration
}

func NewResponseTimeGraph() ResponseTimeGraph {
	return ResponseTimeGraph{
		buffer:    10,
		height:    20,
		width:     79,
		offset:    20,
		precision: 3,
		data:      []float64{},
	}
}

func (g *ResponseTimeGraph) Plot(elapsed time.Duration) {
	if elapsed > g.max {
		g.max = elapsed
	}
	if len(g.data) > g.buffer {
		g.data = g.data[len(g.data)-g.buffer:]
	}
	sample := float64(elapsed) / float64(time.Millisecond)
	g.data = append(g.data, sample)
	graph := asciigraph.Plot(g.data,
		asciigraph.Height(g.height),
		asciigraph.Width(g.width),
		asciigraph.Offset(g.offset),
		asciigraph.Precision(g.precision),
		asciigraph.Caption(fmt.Sprintf("Response time [%.3f ms], max [%.3f ms]", sample, float64(g.max)/float64(time.Millisecond))),
	)

	asciigraph.Clear()
	fmt.Println(graph)
}

func main() {
	kind := os.Args[1]
	addr := os.Args[2]

	// Resolve TCP Address
	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		panic("Unable to resolve IP")
	}
	if kind == "s" {
		err = server(tcpAddr)

	} else if kind == "c" {
		err = client(tcpAddr)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}
