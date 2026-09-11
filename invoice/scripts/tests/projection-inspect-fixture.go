package main

import (
 "fmt"
 "os"
 "text/template"
)

// Docker network inspect exposes EndpointResource, while aliases belong to
// the container's NetworkSettings.Networks EndpointSettings projection.
type EndpointResource struct { Name, EndpointID, MacAddress, IPv4Address, IPv6Address string }
type EndpointSettings struct { Aliases []string }
type NetworkInspect struct { Containers map[string]EndpointResource }
type ContainerInspect struct { NetworkSettings struct { Networks map[string]EndpointSettings } }

func main() {
 a := os.Args[1:]
 if len(a) == 3 && a[0] == "container" && a[1] == "inspect" { return }
 if len(a) != 5 || a[1] != "inspect" || a[2] != "--format" {
  fmt.Fprintln(os.Stderr, "fixture forbids Docker mutation/unknown call"); os.Exit(80)
 }
 var data any
 switch a[0] {
 case "network":
  data = NetworkInspect{map[string]EndpointResource{"fixture-id": {Name:"fixture-db"}}}
 case "container":
  if os.Getenv("ATTACHMENT_CASE") == "query-error" { os.Exit(74) }
  aliases := []string{"required-alias"}
  if os.Getenv("ATTACHMENT_CASE") == "missing-alias" { aliases = []string{"other-alias"} }
  item := ContainerInspect{}
  item.NetworkSettings.Networks = map[string]EndpointSettings{"fixture-network": {Aliases:aliases}}
  data = item
 default: os.Exit(80)
 }
 t, err := template.New("inspect-shape").Parse(a[3])
 if err == nil { err = t.Execute(os.Stdout, data) }
 if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(65) }
}
