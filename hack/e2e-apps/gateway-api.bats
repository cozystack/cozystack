#!/usr/bin/env bats

@test "test gateway api tcp load balancer in tenant cluster" {
  name=test

  # Create backend deployments
  kubectl apply -f- <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: backend1
  namespace: tenant-test
spec:
  replicas: 1
  selector:
    matchLabels:
      app: backend
  template:
    metadata:
      labels:
        app: backend
        backend: backend1
    spec:
      containers:
      - name: nginx
        image: nginx:alpine
        ports:
        - containerPort: 80
        command: ["nginx", "-g", "daemon off;"]
        volumeMounts:
        - name: config
          mountPath: /usr/share/nginx/html
      volumes:
      - name: config
        configMap:
          name: backend1-config
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: backend1-config
  namespace: tenant-test
data:
  index.html: |
    Hello from backend1
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: backend2
  namespace: tenant-test
spec:
  replicas: 1
  selector:
    matchLabels:
      app: backend
  template:
    metadata:
      labels:
        app: backend
        backend: backend2
    spec:
      containers:
      - name: nginx
        image: nginx:alpine
        ports:
        - containerPort: 80
        command: ["nginx", "-g", "daemon off;"]
        volumeMounts:
        - name: config
          mountPath: /usr/share/nginx/html
      volumes:
      - name: config
        configMap:
          name: backend2-config
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: backend2-config
  namespace: tenant-test
data:
  index.html: |
    Hello from backend2
EOF

  # Create Gateway
  kubectl apply -f- <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: ${name}-gateway
  namespace: tenant-test
spec:
  gatewayClassName: cilium
  listeners:
  - name: tcp
    hostname: "*.example.com"
    port: 80
    protocol: TCP
EOF

  # Create TCPRoute
  kubectl apply -f- <<EOF
apiVersion: gateway.networking.k8s.io/v1alpha2
kind: TCPRoute
metadata:
  name: ${name}-tcproute
  namespace: tenant-test
spec:
  parentRefs:
  - name: ${name}-gateway
  rules:
  - backendRefs:
    - name: backend-svc
      port: 80
EOF

  # Create backend service
  kubectl apply -f- <<EOF
apiVersion: v1
kind: Service
metadata:
  name: backend-svc
  namespace: tenant-test
spec:
  ports:
  - port: 80
    targetPort: 80
  selector:
    app: backend
EOF

  # Wait for deployments
  kubectl wait deployment backend1 --namespace tenant-test --timeout=60s --for=jsonpath='{.status.readyReplicas}'=1
  kubectl wait deployment backend2 --namespace tenant-test --timeout=60s --for=jsonpath='{.status.readyReplicas}'=1

  # Wait for Gateway to be ready
  kubectl wait gateway ${name}-gateway --namespace tenant-test --timeout=120s --for=jsonpath='{.status.conditions[?(@.type=="Ready")].status}'=True

  # Wait for TCPRoute to be ready
  kubectl wait tcproute ${name}-tcproute --namespace tenant-test --timeout=60s --for=jsonpath='{.status.parents[0].conditions[?(@.type=="Accepted")].status}'=True

  # Get Gateway IP (assuming LoadBalancer type)
  gateway_ip=$(kubectl get gateway ${name}-gateway -n tenant-test -o jsonpath='{.status.addresses[0].value}')

  # If no external IP, use port-forward to envoy service
  if [ -z "$gateway_ip" ]; then
    kubectl port-forward service/cilium-gateway-${name}-gateway -n tenant-test 8080:80 > /dev/null 2>&1 &
    PF_PID=$!
    timeout 30 sh -ec "until nc -z localhost 8080; do sleep 1; done"
    test_url="http://localhost:8080"
  else
    test_url="http://$gateway_ip"
  fi

  # Test load balancing
  responses=""
  for i in {1..10}; do
    response=$(curl -s $test_url)
    responses="$responses$response\n"
  done
  # Check that both backends are hit
  echo -e "$responses" | grep -q "Hello from backend1"
  echo -e "$responses" | grep -q "Hello from backend2"

  # Cleanup
  if [ -n "$PF_PID" ]; then kill $PF_PID 2>/dev/null || true; fi
  kubectl delete deployment backend1 backend2 --namespace tenant-test
  kubectl delete configmap backend1-config backend2-config --namespace tenant-test
  kubectl delete gateway ${name}-gateway --namespace tenant-test
  kubectl delete tcproute ${name}-tcproute --namespace tenant-test
  kubectl delete service backend-svc --namespace tenant-test
}