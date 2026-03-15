/*
Copyright 2025 The Cozystack Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func newTabWriter() *tabwriter.Writer {
	return tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
}

func printApplications(items []unstructured.Unstructured) {
	w := newTabWriter()
	defer w.Flush()

	fmt.Fprintln(w, "NAME\tVERSION\tREADY\tSTATUS")
	for _, item := range items {
		name := item.GetName()
		version, _, _ := unstructured.NestedString(item.Object, "appVersion")
		ready, status := extractCondition(item)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", name, version, ready, truncate(status, 48))
	}
}

func printNamespaces(items []unstructured.Unstructured) {
	w := newTabWriter()
	defer w.Flush()

	fmt.Fprintln(w, "NAME")
	for _, item := range items {
		fmt.Fprintln(w, item.GetName())
	}
}

func printModules(items []unstructured.Unstructured) {
	w := newTabWriter()
	defer w.Flush()

	fmt.Fprintln(w, "NAME\tVERSION\tREADY\tSTATUS")
	for _, item := range items {
		name := item.GetName()
		version, _, _ := unstructured.NestedString(item.Object, "appVersion")
		ready, status := extractCondition(item)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", name, version, ready, truncate(status, 48))
	}
}

func printPVCs(items []unstructured.Unstructured) {
	w := newTabWriter()
	defer w.Flush()

	fmt.Fprintln(w, "NAME\tSTATUS\tVOLUME\tCAPACITY\tSTORAGECLASS")
	for _, item := range items {
		name := item.GetName()
		phase, _, _ := unstructured.NestedString(item.Object, "status", "phase")
		volume, _, _ := unstructured.NestedString(item.Object, "spec", "volumeName")
		capacity := ""
		if cap, ok, _ := unstructured.NestedStringMap(item.Object, "status", "capacity"); ok {
			capacity = cap["storage"]
		}
		sc, _, _ := unstructured.NestedString(item.Object, "spec", "storageClassName")
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", name, phase, volume, capacity, sc)
	}
}

func printSecrets(items []unstructured.Unstructured) {
	w := newTabWriter()
	defer w.Flush()

	fmt.Fprintln(w, "NAME\tTYPE\tDATA")
	for _, item := range items {
		name := item.GetName()
		secretType, _, _ := unstructured.NestedString(item.Object, "type")
		data, _, _ := unstructured.NestedMap(item.Object, "data")
		fmt.Fprintf(w, "%s\t%s\t%d\n", name, secretType, len(data))
	}
}

func printServices(items []unstructured.Unstructured) {
	w := newTabWriter()
	defer w.Flush()

	fmt.Fprintln(w, "NAME\tTYPE\tCLUSTER-IP\tEXTERNAL-IP\tPORTS")
	for _, item := range items {
		name := item.GetName()
		svcType, _, _ := unstructured.NestedString(item.Object, "spec", "type")
		clusterIP, _, _ := unstructured.NestedString(item.Object, "spec", "clusterIP")

		externalIP := "<none>"
		if lbIngress, ok, _ := unstructured.NestedSlice(item.Object, "status", "loadBalancer", "ingress"); ok && len(lbIngress) > 0 {
			var ips []string
			for _, ingress := range lbIngress {
				if m, ok := ingress.(map[string]interface{}); ok {
					if ip, ok := m["ip"].(string); ok {
						ips = append(ips, ip)
					} else if hostname, ok := m["hostname"].(string); ok {
						ips = append(ips, hostname)
					}
				}
			}
			if len(ips) > 0 {
				externalIP = strings.Join(ips, ",")
			}
		}

		ports := formatPorts(item)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", name, svcType, clusterIP, externalIP, ports)
	}
}

func printIngresses(items []unstructured.Unstructured) {
	w := newTabWriter()
	defer w.Flush()

	fmt.Fprintln(w, "NAME\tCLASS\tHOSTS\tADDRESS")
	for _, item := range items {
		name := item.GetName()
		class, _, _ := unstructured.NestedString(item.Object, "spec", "ingressClassName")

		var hosts []string
		if rules, ok, _ := unstructured.NestedSlice(item.Object, "spec", "rules"); ok {
			for _, rule := range rules {
				if m, ok := rule.(map[string]interface{}); ok {
					if host, ok := m["host"].(string); ok {
						hosts = append(hosts, host)
					}
				}
			}
		}
		hostsStr := "<none>"
		if len(hosts) > 0 {
			hostsStr = strings.Join(hosts, ",")
		}

		address := ""
		if lbIngress, ok, _ := unstructured.NestedSlice(item.Object, "status", "loadBalancer", "ingress"); ok && len(lbIngress) > 0 {
			var addrs []string
			for _, ingress := range lbIngress {
				if m, ok := ingress.(map[string]interface{}); ok {
					if ip, ok := m["ip"].(string); ok {
						addrs = append(addrs, ip)
					} else if hostname, ok := m["hostname"].(string); ok {
						addrs = append(addrs, hostname)
					}
				}
			}
			address = strings.Join(addrs, ",")
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", name, class, hostsStr, address)
	}
}

func printWorkloads(items []unstructured.Unstructured) {
	w := newTabWriter()
	defer w.Flush()

	fmt.Fprintln(w, "NAME\tKIND\tTYPE\tVERSION\tAVAILABLE\tOBSERVED\tOPERATIONAL")
	for _, item := range items {
		name := item.GetName()
		kind, _, _ := unstructured.NestedString(item.Object, "spec", "kind")
		wType, _, _ := unstructured.NestedString(item.Object, "spec", "type")
		version, _, _ := unstructured.NestedString(item.Object, "spec", "version")
		available, _, _ := unstructured.NestedInt64(item.Object, "status", "availableReplicas")
		observed, _, _ := unstructured.NestedInt64(item.Object, "status", "observedReplicas")
		operational, ok, _ := unstructured.NestedBool(item.Object, "status", "operational")
		opStr := ""
		if ok {
			opStr = fmt.Sprintf("%t", operational)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%d\t%s\n", name, kind, wType, version, available, observed, opStr)
	}
}

func printNoResources(w io.Writer, resourceType string) {
	fmt.Fprintf(w, "No %s found\n", resourceType)
}

func extractCondition(item unstructured.Unstructured) (string, string) {
	conditions, ok, _ := unstructured.NestedSlice(item.Object, "status", "conditions")
	if !ok {
		return "Unknown", ""
	}
	for _, c := range conditions {
		cond, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if cond["type"] == "Ready" {
			ready, _ := cond["status"].(string)
			message, _ := cond["message"].(string)
			return ready, message
		}
	}
	return "Unknown", ""
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func formatPorts(item unstructured.Unstructured) string {
	ports, ok, _ := unstructured.NestedSlice(item.Object, "spec", "ports")
	if !ok || len(ports) == 0 {
		return "<none>"
	}
	var parts []string
	for _, p := range ports {
		port, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		portNum, _, _ := unstructured.NestedInt64(port, "port")
		protocol, _, _ := unstructured.NestedString(port, "protocol")
		if protocol == "" {
			protocol = "TCP"
		}
		nodePort, _, _ := unstructured.NestedInt64(port, "nodePort")
		if nodePort > 0 {
			parts = append(parts, fmt.Sprintf("%d:%d/%s", portNum, nodePort, protocol))
		} else {
			parts = append(parts, fmt.Sprintf("%d/%s", portNum, protocol))
		}
	}
	return strings.Join(parts, ",")
}
