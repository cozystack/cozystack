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

package proxmoxnetworkcontroller

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func jsonEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got invalid JSON %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want invalid JSON: %v", err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("JSON differs\n got: %s\nwant: %s", got, want)
	}
}

// The operations ensure sends to create a group, in the RFC 7047 encoding
// ovsdb-server accepts: no member an op does not take, where always present,
// named-uuids, sets (one-element ones as the bare atom) and maps.
func TestOVSDBOperationEncoding(t *testing.T) {
	ops := []ovsdbOp{
		{Op: "select", Table: tableLRP, Where: []ovsdbCondition{ovsdbWhere(colName, "==", "vpc-x-subnet-a")}, Columns: []string{colUUID, colHAChassisGroup, colGatewayChassis}},
		{Op: "insert", Table: tableHAChassis, UUIDName: "pxchassis0", Row: map[string]any{colChassisName: "ch-w1", colPriority: 100}},
		{Op: "insert", Table: tableHAChassis, UUIDName: "pxchassis1", Row: map[string]any{colChassisName: "ch-w2", colPriority: 90}},
		{Op: "insert", Table: tableHAChassisGrp, UUIDName: "pxgroup", Row: map[string]any{
			colName:        "px-vpc-x-subnet-a",
			colExternalIDs: ovsMap{"owner": "proxmox-network", "network": "tenant-a/subnet-a"},
			colHAChassis:   ovsSet{ovsNamedUUID("pxchassis0"), ovsNamedUUID("pxchassis1")},
		}},
		{Op: "wait", Table: tableLRP, Where: []ovsdbCondition{ovsdbWhere(colUUID, "==", ovsUUID("8e7c4912-7215-4752-8556-c09d63f255a7"))},
			Columns: []string{colHAChassisGroup, colGatewayChassis}, Until: "==",
			WaitRows: []map[string]any{{colHAChassisGroup: ovsSet{}, colGatewayChassis: ovsSet{}}}},
		{Op: "update", Table: tableLRP, Where: []ovsdbCondition{ovsdbWhere(colUUID, "==", ovsUUID("8e7c4912-7215-4752-8556-c09d63f255a7"))},
			Row: map[string]any{colHAChassisGroup: ovsNamedUUID("pxgroup")}},
		{Op: "mutate", Table: tableHAChassisGrp, Where: []ovsdbCondition{ovsdbWhere(colUUID, "==", ovsUUID("83e1d615-dff9-4ecd-bdc9-84cd865d44d5"))},
			Mutations: []ovsdbMutation{ovsdbMutate(colHAChassis, "delete", ovsSet{ovsUUID("1e3e17fb-2c1f-4b8f-9eca-9a9279a15d3d")})}},
		{Op: "update", Table: tableLRP, Where: []ovsdbCondition{ovsdbWhere(colUUID, "==", ovsUUID("8e7c4912-7215-4752-8556-c09d63f255a7"))},
			Row: map[string]any{colHAChassisGroup: ovsSet{}}},
		{Op: "delete", Table: tableHAChassisGrp},
		{Op: "select", Table: tableHAChassisGrp, Where: []ovsdbCondition{ovsdbWhere(colExternalIDs, "includes", ovsMap{"owner": "proxmox-network"})}},
	}
	got, err := json.Marshal(ops)
	if err != nil {
		t.Fatal(err)
	}
	jsonEqual(t, got, `[
	  {"op":"select","table":"Logical_Router_Port","where":[["name","==","vpc-x-subnet-a"]],"columns":["_uuid","ha_chassis_group","gateway_chassis"]},
	  {"op":"insert","table":"HA_Chassis","uuid-name":"pxchassis0","row":{"chassis_name":"ch-w1","priority":100}},
	  {"op":"insert","table":"HA_Chassis","uuid-name":"pxchassis1","row":{"chassis_name":"ch-w2","priority":90}},
	  {"op":"insert","table":"HA_Chassis_Group","uuid-name":"pxgroup","row":{
	    "name":"px-vpc-x-subnet-a",
	    "external_ids":["map",[["network","tenant-a/subnet-a"],["owner","proxmox-network"]]],
	    "ha_chassis":["set",[["named-uuid","pxchassis0"],["named-uuid","pxchassis1"]]]}},
	  {"op":"wait","table":"Logical_Router_Port","where":[["_uuid","==",["uuid","8e7c4912-7215-4752-8556-c09d63f255a7"]]],
	    "columns":["ha_chassis_group","gateway_chassis"],"until":"==","timeout":0,
	    "rows":[{"ha_chassis_group":["set",[]],"gateway_chassis":["set",[]]}]},
	  {"op":"update","table":"Logical_Router_Port","where":[["_uuid","==",["uuid","8e7c4912-7215-4752-8556-c09d63f255a7"]]],
	    "row":{"ha_chassis_group":["named-uuid","pxgroup"]}},
	  {"op":"mutate","table":"HA_Chassis_Group","where":[["_uuid","==",["uuid","83e1d615-dff9-4ecd-bdc9-84cd865d44d5"]]],
	    "mutations":[["ha_chassis","delete",["uuid","1e3e17fb-2c1f-4b8f-9eca-9a9279a15d3d"]]]},
	  {"op":"update","table":"Logical_Router_Port","where":[["_uuid","==",["uuid","8e7c4912-7215-4752-8556-c09d63f255a7"]]],
	    "row":{"ha_chassis_group":["set",[]]}},
	  {"op":"delete","table":"HA_Chassis_Group","where":[]},
	  {"op":"select","table":"HA_Chassis_Group","where":[["external_ids","includes",["map",[["owner","proxmox-network"]]]]]}
	]`)
}

// A reply as ovsdb-server writes it: a one-element set as the bare atom, an
// empty set, a map.
func TestOVSDBRowDecoding(t *testing.T) {
	var res []ovsdbResult
	reply := `[{"rows":[
	  {"_uuid":["uuid","83e1d615-dff9-4ecd-bdc9-84cd865d44d5"],"name":"px-vpc-2ac48e-subnet-d76a7c3e",
	   "external_ids":["map",[["network","tenant-a/x"],["owner","proxmox-network"]]],
	   "ha_chassis":["set",[["uuid","1e3e17fb-2c1f-4b8f-9eca-9a9279a15d3d"],["uuid","db7c8442-0c60-46b6-b21b-2026ce53d1ab"]]]},
	  {"_uuid":["uuid","56df2e7a-fdee-4078-8303-5c8d6525a2f9"],"name":"px-one","external_ids":["map",[]],
	   "ha_chassis":["uuid","00626b1b-c68d-4aa2-b32f-5fd3c0d91dc8"],"priority":20},
	  {"_uuid":["uuid","b9a91fd3-3cda-48dd-bd16-64f53ccc194b"],"ha_chassis":["set",[]],"ha_chassis_group":["set",[]]}
	]},{"uuid":["uuid","8e7c4912-7215-4752-8556-c09d63f255a7"]},{"count":2},null]`
	if err := json.Unmarshal([]byte(reply), &res); err != nil {
		t.Fatal(err)
	}
	rows := res[0].Rows
	g := parseGroup(rows[0])
	if g.UUID != "83e1d615-dff9-4ecd-bdc9-84cd865d44d5" || g.Name != "px-vpc-2ac48e-subnet-d76a7c3e" ||
		g.ExternalIDs["owner"] != "proxmox-network" || g.ExternalIDs["network"] != "tenant-a/x" || len(g.Chassis) != 2 {
		t.Fatalf("group = %+v", g)
	}
	if u := rows[1].uuids(colHAChassis); len(u) != 1 || u[0] != "00626b1b-c68d-4aa2-b32f-5fd3c0d91dc8" {
		t.Fatalf("one-element set = %v", u)
	}
	if rows[1].integer(colPriority) != 20 || len(rows[1].strMap(colExternalIDs)) != 0 {
		t.Fatalf("row = %v", rows[1])
	}
	if len(rows[2].uuids(colHAChassis)) != 0 || rows[2].uuid(colHAChassisGroup) != "" {
		t.Fatalf("empty sets = %v", rows[2])
	}
	if res[1].UUID == nil || *res[1].UUID != "8e7c4912-7215-4752-8556-c09d63f255a7" || res[2].Count != 2 || res[3].Error != "" {
		t.Fatalf("results = %+v", res)
	}
}

// ovsdbServer is a scripted OVSDB peer: each accepted connection runs
// handle with a line-oriented JSON reader and the raw connection.
type ovsdbServer struct {
	ln       net.Listener
	accepted atomic.Int32
}

func startOVSDBServer(t *testing.T, ln net.Listener, handle func(t *testing.T, dec *json.Decoder, conn net.Conn)) *ovsdbServer {
	t.Helper()
	s := &ovsdbServer{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			s.accepted.Add(1)
			go func() {
				defer func() { _ = conn.Close() }()
				handle(t, json.NewDecoder(bufio.NewReader(conn)), conn)
			}()
		}
	}()
	return s
}

func (s *ovsdbServer) address(scheme string) string {
	return scheme + ":" + s.ln.Addr().String()
}

type rpcRequest struct {
	ID     json.RawMessage   `json:"id"`
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
	Result json.RawMessage   `json:"result"`
}

func send(t *testing.T, conn net.Conn, msg string) {
	t.Helper()
	if _, err := conn.Write([]byte(msg)); err != nil {
		t.Error(err)
	}
}

func TestNBClientSpeaksJSONRPC(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan rpcRequest, 10)
	srv := startOVSDBServer(t, ln, func(t *testing.T, dec *json.Decoder, conn net.Conn) {
		// First request: the server interleaves an echo, a notification and
		// a reply to a request the client never sent before answering.
		var req rpcRequest
		if err := dec.Decode(&req); err != nil {
			return
		}
		requests <- req
		send(t, conn, `{"id":"echo","method":"echo","params":[]}`)
		var echo rpcRequest
		if err := dec.Decode(&echo); err != nil {
			t.Error(err)
			return
		}
		requests <- echo
		send(t, conn, `{"id":null,"method":"update","params":[null,{}]}`)
		send(t, conn, `{"id":999,"result":[],"error":null}`)
		send(t, conn, `{"id":`+string(req.ID)+`,"error":null,"result":[
		  {"rows":[{"_uuid":["uuid","83e1d615-dff9-4ecd-bdc9-84cd865d44d5"],"name":"px-x",
		    "external_ids":["map",[["owner","proxmox-network"]]],
		    "ha_chassis":["set",[["uuid","c1"],["uuid","c2"]]]}]},
		  {"uuid":["uuid","1e3e17fb-2c1f-4b8f-9eca-9a9279a15d3d"]}]}`)

		// Second request on the same connection: a failed operation.
		if err := dec.Decode(&req); err != nil {
			return
		}
		requests <- req
		send(t, conn, `{"id":`+string(req.ID)+`,"error":null,"result":[{"count":1},
		  {"error":"referential integrity violation","details":"cannot delete HA_Chassis_Group row because of 1 remaining reference(s)"},null]}`)

		// Third: the connection drops.
		if err := dec.Decode(&req); err != nil {
			return
		}
		requests <- req
	})

	c, err := NewNBClient(srv.address("tcp"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	ops := []ovsdbOp{
		{Op: "select", Table: tableHAChassisGrp, Where: []ovsdbCondition{ovsdbWhere(colName, "==", "px-x")}},
		{Op: "insert", Table: tableHAChassisGrp, UUIDName: "pxgroup", Row: map[string]any{colName: "px-y", colHAChassis: ovsSet{}}},
	}
	res, err := c.Transact(ctx, ops...)
	if err != nil {
		t.Fatal(err)
	}
	req := <-requests
	if req.Method != "transact" || len(req.Params) != 3 || string(req.Params[0]) != `"OVN_Northbound"` {
		t.Fatalf("request = %+v", req)
	}
	jsonEqual(t, req.Params[2], `{"op":"insert","table":"HA_Chassis_Group","uuid-name":"pxgroup","row":{"name":"px-y","ha_chassis":["set",[]]}}`)
	if echo := <-requests; string(echo.ID) != `"echo"` || string(echo.Result) != `[]` {
		t.Fatalf("echo reply = %+v", echo)
	}
	if g := parseGroup(res[0].Rows[0]); g.Name != "px-x" || len(g.Chassis) != 2 || g.ExternalIDs["owner"] != "proxmox-network" {
		t.Fatalf("group = %+v", g)
	}
	if res[1].UUID == nil || *res[1].UUID != "1e3e17fb-2c1f-4b8f-9eca-9a9279a15d3d" {
		t.Fatalf("insert result = %+v", res[1])
	}

	_, err = c.Transact(ctx, ovsdbOp{Op: "update", Table: tableLRP, Row: map[string]any{}}, ovsdbOp{Op: "delete", Table: tableHAChassisGrp})
	var opErr *ovsdbOpError
	if !errors.As(err, &opErr) || opErr.Index != 1 || opErr.Op != "delete" || opErr.Kind != "referential integrity violation" {
		t.Fatalf("err = %v", err)
	}
	<-requests
	if srv.accepted.Load() != 1 {
		t.Fatal("an operation error must not drop the connection")
	}

	// The server hangs up after reading: a transport error, then a redial.
	if _, err := c.Transact(ctx, ovsdbOp{Op: "select", Table: tableLRP}); err == nil || errors.As(err, &opErr) {
		t.Fatalf("err = %v, want a transport error", err)
	}
	<-requests
	_, _ = c.Transact(ctx, ovsdbOp{Op: "select", Table: tableLRP})
	if srv.accepted.Load() != 2 {
		t.Fatalf("accepted %d connections, want a redial after the drop", srv.accepted.Load())
	}
}

// An idle connection is not reused: the server may have given up on it
// after unanswered echo probes.
func TestNBClientRedialsAfterIdling(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := startOVSDBServer(t, ln, func(t *testing.T, dec *json.Decoder, conn net.Conn) {
		for {
			var req rpcRequest
			if err := dec.Decode(&req); err != nil {
				return
			}
			send(t, conn, `{"id":`+string(req.ID)+`,"error":null,"result":[{"rows":[]}]}`)
		}
	})
	c, err := NewNBClient(srv.address("tcp"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for range 3 {
		if _, err := c.Transact(context.Background(), ovsdbOp{Op: "select", Table: tableLRP}); err != nil {
			t.Fatal(err)
		}
	}
	if n := srv.accepted.Load(); n != 1 {
		t.Fatalf("back-to-back transactions opened %d connections, want 1", n)
	}
	c.maxIdle = 20 * time.Millisecond
	time.Sleep(50 * time.Millisecond)
	if _, err := c.Transact(context.Background(), ovsdbOp{Op: "select", Table: tableLRP}); err != nil {
		t.Fatal(err)
	}
	if n := srv.accepted.Load(); n != 2 {
		t.Fatalf("accepted %d connections, want a new one after idling", n)
	}
}

func TestNBClientTimesOut(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := startOVSDBServer(t, ln, func(t *testing.T, dec *json.Decoder, conn net.Conn) {
		var req rpcRequest
		_ = dec.Decode(&req)
		time.Sleep(2 * time.Second)
	})
	c, err := NewNBClient(srv.address("tcp"), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.Transact(ctx, ovsdbOp{Op: "select", Table: tableLRP}); err == nil {
		t.Fatal("a silent server must time out")
	}
	if time.Since(start) > time.Second {
		t.Fatal("the context deadline was not honoured")
	}
}

// pki is a CA and the certificates it signed, the way Helm's genSignedCert
// issues Kube-OVN's: common name "ovn", no host name, serverAuth and
// clientAuth. clientOnly is one good for clients only.
type pki struct {
	caPEM, certPEM, keyPEM []byte
	server, clientOnly     tls.Certificate
	pool                   *x509.CertPool
}

func newPKI(t *testing.T) pki {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "switchca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	leaf := func(serial int64, usage ...x509.ExtKeyUsage) ([]byte, []byte) {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "ovn"},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
			ExtKeyUsage: usage}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		kder, _ := x509.MarshalECPrivateKey(key)
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder})
	}
	p := pki{caPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), pool: x509.NewCertPool()}
	p.pool.AddCert(ca)
	both := []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}
	p.certPEM, p.keyPEM = leaf(2, both...)
	scert, skey := leaf(3, both...)
	p.server, err = tls.X509KeyPair(scert, skey)
	if err != nil {
		t.Fatal(err)
	}
	ccert, ckey := leaf(4, x509.ExtKeyUsageClientAuth)
	p.clientOnly, err = tls.X509KeyPair(ccert, ckey)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func tlsSecret(p pki, version string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "cozy-kubeovn", Name: "kube-ovn-tls", ResourceVersion: version},
		Data:       map[string][]byte{"cacert": p.caPEM, "cert": p.certPEM, "key": p.keyPEM},
	}
}

func TestNBClientOverTLSWithTheKubeOVNSecret(t *testing.T) {
	good := newPKI(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{good.server},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    good.pool,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := startOVSDBServer(t, ln, func(t *testing.T, dec *json.Decoder, conn net.Conn) {
		for {
			var req rpcRequest
			if err := dec.Decode(&req); err != nil {
				return
			}
			send(t, conn, `{"id":`+string(req.ID)+`,"error":null,"result":[{"rows":[]}]}`)
		}
	})

	c := newClient(t, tlsSecret(good, ""))
	src := &SecretTLSSource{Reader: c, Secret: client.ObjectKey{Namespace: "cozy-kubeovn", Name: "kube-ovn-tls"}}
	nb, err := NewNBClient(srv.address("ssl"), src)
	if err != nil {
		t.Fatal(err)
	}
	defer nb.Close()
	// Recovery is checked right after a failed handshake; the backoff has
	// its own test.
	nb.connectBackoff = 0
	// The server certificate names no host, as Kube-OVN's does not: the
	// chain is checked against the CA, the name is not.
	if _, err := nb.Transact(context.Background(), ovsdbOp{Op: "select", Table: tableLRP}); err != nil {
		t.Fatal(err)
	}
	cached, _ := src.TLSConfig(context.Background())

	// The secret is rotated to another CA: the next connection reads it
	// again and the handshake fails, which drops the cached configuration.
	other := newPKI(t)
	sec := &corev1.Secret{}
	_ = c.Get(context.Background(), src.Secret, sec)
	sec.Data = tlsSecret(other, "").Data
	if err := c.Update(context.Background(), sec); err != nil {
		t.Fatal(err)
	}
	if again, _ := src.TLSConfig(context.Background()); again == cached {
		t.Fatal("a changed secret must be parsed again")
	}
	nb.Close()
	_, err = nb.Transact(context.Background(), ovsdbOp{Op: "select", Table: tableLRP})
	if err == nil || !strings.Contains(err.Error(), "TLS handshake") {
		t.Fatalf("err = %v, want a failed handshake", err)
	}
	src.mu.Lock()
	invalidated := src.config == nil
	src.mu.Unlock()
	if !invalidated {
		t.Fatal("a failed handshake must drop the cached TLS configuration")
	}
	if strings.Contains(err.Error(), "BEGIN") {
		t.Fatal("an error must never carry key material")
	}

	// The right material again: the client recovers.
	sec.Data = tlsSecret(good, "").Data
	if err := c.Update(context.Background(), sec); err != nil {
		t.Fatal(err)
	}
	if _, err := nb.Transact(context.Background(), ovsdbOp{Op: "select", Table: tableLRP}); err != nil {
		t.Fatal(err)
	}
}

func TestSecretTLSSourceRefusesIncompleteMaterial(t *testing.T) {
	p := newPKI(t)
	sec := tlsSecret(p, "")
	delete(sec.Data, "key")
	c := newClient(t, sec)
	src := &SecretTLSSource{Reader: c, Secret: client.ObjectKey{Namespace: "cozy-kubeovn", Name: "kube-ovn-tls"}}
	if _, err := src.TLSConfig(context.Background()); err == nil || !strings.Contains(err.Error(), `no "key" key`) {
		t.Fatalf("err = %v", err)
	}
	sec.Data["key"] = []byte("-----BEGIN EC PRIVATE KEY-----\nbm90IGEga2V5\n-----END EC PRIVATE KEY-----\n")
	if err := c.Update(context.Background(), sec); err != nil {
		t.Fatal(err)
	}
	_, err := src.TLSConfig(context.Background())
	if err == nil || strings.Contains(err.Error(), "BEGIN") || strings.Contains(err.Error(), "bm90") {
		t.Fatalf("err = %v, want an error without the material", err)
	}
	missing := &SecretTLSSource{Reader: c, Secret: client.ObjectKey{Namespace: "cozy-kubeovn", Name: "absent"}}
	if _, err := missing.TLSConfig(context.Background()); err == nil {
		t.Fatal("a missing secret must fail")
	}
}

// A server that is not Kube-OVN's NB is refused, even one that asks for no
// client certificate, so the handshake itself would go through.
func TestNBClientRejectsARogueServer(t *testing.T) {
	good := newPKI(t)
	c := newClient(t, tlsSecret(good, ""))
	src := &SecretTLSSource{Reader: c, Secret: client.ObjectKey{Namespace: "cozy-kubeovn", Name: "kube-ovn-tls"}}
	for name, cert := range map[string]tls.Certificate{
		"certificate of another CA":         newPKI(t).server,
		"certificate not good for a server": good.clientOnly,
	} {
		t.Run(name, func(t *testing.T) {
			ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, ClientAuth: tls.NoClientCert})
			if err != nil {
				t.Fatal(err)
			}
			srv := startOVSDBServer(t, ln, func(t *testing.T, dec *json.Decoder, conn net.Conn) {
				var req rpcRequest
				if err := dec.Decode(&req); err != nil {
					return
				}
				send(t, conn, `{"id":`+string(req.ID)+`,"error":null,"result":[{"rows":[]}]}`)
			})
			nb, err := NewNBClient(srv.address("ssl"), src)
			if err != nil {
				t.Fatal(err)
			}
			defer nb.Close()
			_, err = nb.Transact(context.Background(), ovsdbOp{Op: "select", Table: tableLRP})
			if err == nil || !strings.Contains(err.Error(), "OVN NB certificate") {
				t.Fatalf("err = %v, want the server certificate refused", err)
			}
		})
	}
}

// A connection is not reused past its age, however busy, so that new ones
// follow the RAFT leader behind the Service.
func TestNBClientRedialsAfterMaxAge(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := startOVSDBServer(t, ln, func(t *testing.T, dec *json.Decoder, conn net.Conn) {
		for {
			var req rpcRequest
			if err := dec.Decode(&req); err != nil {
				return
			}
			send(t, conn, `{"id":`+string(req.ID)+`,"error":null,"result":[{"rows":[]}]}`)
		}
	})
	c, err := NewNBClient(srv.address("tcp"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.maxIdle, c.maxAge = time.Hour, 50*time.Millisecond
	for range 2 {
		if _, err := c.Transact(context.Background(), ovsdbOp{Op: "select", Table: tableLRP}); err != nil {
			t.Fatal(err)
		}
	}
	if n := srv.accepted.Load(); n != 1 {
		t.Fatalf("accepted %d connections, want 1", n)
	}
	time.Sleep(80 * time.Millisecond)
	if _, err := c.Transact(context.Background(), ovsdbOp{Op: "select", Table: tableLRP}); err != nil {
		t.Fatal(err)
	}
	if n := srv.accepted.Load(); n != 2 {
		t.Fatalf("accepted %d connections, want a new one past the age limit", n)
	}
}

// An NB that does not answer costs one dial timeout per backoff, not one per
// caller, and the dial timeout is the short one.
func TestNBClientFailsFastAfterAFailedConnect(t *testing.T) {
	c, err := NewNBClient("tcp:192.0.2.1:6641", nil)
	if err != nil {
		t.Fatal(err)
	}
	var dials atomic.Int32
	c.dialTimeout = 50 * time.Millisecond
	c.dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		dials.Add(1)
		<-ctx.Done() // a blackholed address
		return nil, ctx.Err()
	}
	start := time.Now()
	if _, err := c.Transact(context.Background(), ovsdbOp{Op: "select", Table: tableLRP}); err == nil {
		t.Fatal("want a connect error")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("connect took %v, want the dial timeout", d)
	}
	for range 5 {
		if _, err := c.Transact(context.Background(), ovsdbOp{Op: "select", Table: tableLRP}); err == nil || !strings.Contains(err.Error(), "not retrying") {
			t.Fatalf("err = %v, want the cached connect error", err)
		}
	}
	if n := dials.Load(); n != 1 {
		t.Fatalf("dialled %d times within the backoff, want 1", n)
	}
	c.failedUntil = time.Now().Add(-time.Millisecond)
	_, _ = c.Transact(context.Background(), ovsdbOp{Op: "select", Table: tableLRP})
	if n := dials.Load(); n != 2 {
		t.Fatalf("dialled %d times after the backoff, want 2", n)
	}
}

// A caller waiting for another one's transaction gives up with its context.
func TestNBClientWaitEndsWithTheContext(t *testing.T) {
	c, err := NewNBClient("tcp:127.0.0.1:6641", nil)
	if err != nil {
		t.Fatal(err)
	}
	c.sem <- struct{}{} // another call holds the client
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.Transact(ctx, ovsdbOp{Op: "select", Table: tableLRP})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want the context's deadline", err)
		}
	case <-time.After(2 * time.Second):
		<-c.sem // let the stuck call go before failing
		t.Fatal("the wait was not bounded by the context")
	}
	<-c.sem
}
