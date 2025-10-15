#!/bin/bash

#### variables list
cozypkg_version="v1.1.0"
talm_version="v0.13.0"
kubectl_version="v1.33.1"
krew_version="v0.4.5"
helm_version="v3.18.2"
virtctl_version="v1.4.0"
fluxcd_version="2.6.1"
ARCH="$(uname -m | sed -e 's/x86_64/amd64/' -e 's/\(arm\)\(64\)\?.*/\1\2/' -e 's/aarch64$/arm64/')"
echo $ARCH
OS="$(uname | tr '[:upper:]' '[:lower:]')"


function user_setup_env() {
    log "Start setuping user environment"

    install_cozypkg
    install_talm
    install_kubectl
    install_krew
    install_krew_plugins
    install_virtctl
    install_helm
    install_helm_plugins
    install_fluxcd
}

function log() {
    echo "$(date '+%d-%m-%Y %H:%M:%S') - $1"
}

function install_cozypkg() {
    log "Installing cozypkg"

	curl -sSL https://github.com/cozystack/cozypkg/releases/download/${cozypkg_version}/cozypkg-${OS}-${ARCH}.tar.gz | \
	tar xzvf - cozypkg
    sudo mv /tmp/cozypkg /usr/local/bin/cozypkg
    sudo chown 0:0 /usr/local/bin/cozypkg
    sudo chmod 0755 /usr/local/bin/cozypkg
}

function install_talm() {
    log "Installing talm"

    curl -o /tmp/talm -fsL "https://github.com/cozystack/talm/releases/download/${talm_version}/talm-${OS}-${ARCH}"
    sudo mv /tmp/talm /usr/local/bin/talm
    sudo chown 0:0 /usr/local/bin/talm
    sudo chmod 0755 /usr/local/bin/talm
}

function install_kubectl() {
    log "Installing kubectl"

    curl -o /tmp/kubectl -fsLO "https://dl.k8s.io/release/${kubectl_version}/bin/${OS}/${ARCH}/kubectl"
    sudo mv /tmp/kubectl /usr/local/bin/kubectl
    sudo chown 0:0 /usr/local/bin/kubectl
    sudo chmod 0755 /usr/local/bin/kubectl
}

install_krew() {
    log "Installing krew"

    KREW="krew-${OS}_${ARCH}"
    curl -o "/tmp/${KREW}.tar.gz" -fsLO "https://github.com/kubernetes-sigs/krew/releases/download/${krew_version}/${KREW}.tar.gz"
    mkdir /tmp/krew && tar -xzf "/tmp/${KREW}.tar.gz" -C /tmp/krew/
    "/tmp/krew/${KREW}" install krew
    log "configure .bashrc for krew"
    printf '# krew\nexport PATH="${KREW_ROOT:-$HOME/.krew}/bin:$PATH"\n' >> ~/.bashrc
    source ~/.bashrc
}

function install_krew_plugins() {
    log "Installing krew plugins..."

    if [[ ! $(kubectl krew version) ]]; then
        log "krew is not installed, install it first!"
        return 1
    fi

    log "Installing krew plugin: node-shell"
    kubectl krew install node-shell

    log "Installing krew plugin: virt"
    kubectl krew install virt

    log "Installing krew plugin: oidc-login"
    kubectl krew install oidc-login
}

function install_virtctl() {
    log "Installing virtctl"

    curl -o /tmp/virtctl -fsL "https://github.com/kubevirt/kubevirt/releases/download/${virtctl_version}/virtctl-${virtctl_version}-${OS}-${ARCH}"
    sudo mv /tmp/virtctl /usr/local/bin/virtctl
    sudo chown 0:0 /usr/local/bin/virtctl
    sudo chmod 0755 /usr/local/bin/virtctl
}

function install_helm() {
    log "Installing Helm"

    curl -o /tmp/helm.tar.gz -fsL "https://get.helm.sh/helm-${helm_version}-${OS}-${ARCH}.tar.gz"
    mkdir /tmp/helm && tar -xzf /tmp/helm.tar.gz -C /tmp/helm/
    sudo mv "/tmp/helm/${OS}-${ARCH}/helm" /usr/local/bin/helm
    sudo chown 0:0 /usr/local/bin/helm
    sudo chmod 0755 /usr/local/bin/helm
}

function install_helm_plugins() {
    log "Installing Helm plugins..."

    log "Installing Helm plugin: diff"
    helm plugin install https://github.com/databus23/helm-diff
}

function install_fluxcd() {
    log "Installing FluxCD"

    curl -o /tmp/flux.tar.gz -fsL "https://github.com/fluxcd/flux2/releases/download/v${fluxcd_version}/flux_${fluxcd_version}_${OS}_${ARCH}.tar.gz"
    mkdir /tmp/flux && tar -xzf /tmp/flux.tar.gz -C /tmp/flux/
    sudo mv /tmp/flux/flux /usr/local/bin/flux
    sudo chown 0:0 /usr/local/bin/flux
    sudo chmod 0755 /usr/local/bin/flux
}


user_setup_env