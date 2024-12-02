# brpc

![https://img.shields.io/github/v/tag/easysy/brpc](https://img.shields.io/github/v/tag/easysy/brpc)
![https://img.shields.io/github/license/easysy/brpc](https://img.shields.io/github/license/easysy/brpc)

`brpc` is a framework that enables servers to initiate Remote Procedure Calls (RPCs) directly on clients,
flipping the traditional client-to-server communication model.

### Key Features
* **Server-initiated RPCs:** Enables bidirectional communication where servers can initiate RPC calls directly on clients.
* **Centralized Communication Hub:** Provides a single central service (socket) for managing interactions with various client extensions (plugins).
* **Extension Independence:** The central service is designed to be independent of individual plugins,
enabling seamless integration of new extensions or modifications to existing ones without needing server-side changes.
This maintains flexibility and reduces complexity.
* **Single Port Operation:** The entire framework requires only one port for managing multiple extensions,
significantly reducing the risk of port congestion and simplifying network configuration.

### Technical Approach
To ensure independence between the central service and plugins, JSON is used as the primary data protocol,
while GOB handles framework-level communication. This architecture allows for lightweight,
extensible interactions with clients and easy data serialization across different extensions.

## Installation

`brpc` can be installed like any other Go library through `go get`:

```console
go get github.com/easysy/brpc@latest
```

## Getting Started

### Socket (example)

```go
package main

import (
	"fmt"
	"net"
	"time"

	"github.com/easysy/brpc"
)

func main() {
	lis, err := net.Listen("tcp", ":8080")
	if err != nil {
		panic(err)
	}

	s := new(brpc.Socket)
	s.Serve(lis)

	// Read async messages
	go func() {
		for {
			async, e := s.Async()
			if e != nil {
				return
			}
			fmt.Println(async)
		}
	}()

	pn := "your_plugin_name"

	// Wait for your plugin to be connected
	if !s.WaitFor(pn, time.Second*30) {
		panic(pn + "not connected before timeout")
	}

	var resp any
	if resp, err = s.Call("", pn, "ExampleMethod", nil); err != nil {
		panic(err)
	}

	fmt.Println(resp)

	// Get a list of connected plugins
	fmt.Println("list:", s.Connected())

	// If you need, you can stop any plugin
	s.Unplug("1", pn)

	fmt.Println("list:", s.Connected())

	// Shutdown the socket
	if err = s.Shutdown("2"); err != nil {
		panic(err)
	}
}

```

### Plugin (example)

```go
package main

import (
	"context"
	"net"
	"time"

	"github.com/easysy/brpc"
)

type Plugin struct {
	hook chan any
}

func (p *Plugin) UseAsyncHook(hook chan any) {
	p.hook = hook
}

func (p *Plugin) ExampleMethod(_ context.Context, _ struct{}) (string, error) {
	p.hook <- "ExampleMethod started"
	time.Sleep(time.Second * 5)
	p.hook <- "ExampleMethod ended"
	return "Message", nil
}

func main() {
	conn, err := net.Dial("tcp", "127.0.0.1:8080")
	if err != nil {
		panic(err)
	}

	p := new(brpc.Plugin)
	info := new(brpc.PluginInfo)
	info.Name = "your_plugin_name" // The name must be unique for each plugin
	info.Version = "your_plugin_version"
	if err = p.Start(new(Plugin), info, conn, ""); err != nil {
		panic(err)
	}
}

```
