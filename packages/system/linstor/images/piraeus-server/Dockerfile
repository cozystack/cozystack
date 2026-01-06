ARG DISTRO=bookworm
ARG LINSTOR_VERSION

# ------------------------------------------------------------------------------
# Build linstor-server from source
FROM debian:bookworm AS builder

ARG LINSTOR_VERSION
ARG VERSION=${LINSTOR_VERSION}
ARG DISTRO

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get -y upgrade \
 && apt-get -y install build-essential git default-jdk-headless python3-all debhelper wget unzip && \
    wget https://services.gradle.org/distributions/gradle-8.9-bin.zip -O /tmp/gradle.zip && \
    unzip -d /opt /tmp/gradle.zip && \
    rm /tmp/gradle.zip && \
    ln -s /opt/gradle-8.9/bin/gradle /usr/local/bin/gradle

RUN git clone https://github.com/LINBIT/linstor-server.git /linstor-server
WORKDIR /linstor-server
RUN git checkout v${VERSION}

# Apply patches
COPY patches /patches
RUN git apply /patches/*.diff && \
    git config user.email "build@cozystack.io" && \
    git config user.name "Cozystack Builder" && \
    git add -A && \
    git commit -m "Apply patches"

# Initialize git submodules
RUN git submodule update --init --recursive || make check-submods

# Pre-download ALL dependencies before make tarball
# This ensures all transitive dependencies are cached, including optional ones like AWS SDK
RUN ./gradlew getProtoc
RUN ./gradlew generateJava
RUN ./gradlew --no-daemon --gradle-user-home .gradlehome downloadDependencies

# Manually create tarball without removing caches
# make tarball removes .gradlehome/caches/[0-9]* which deletes dependencies
# So we'll do the steps manually but keep the caches
RUN make check-submods versioninfo gen-java FORCE=1 VERSION=${VERSION}
RUN make server/jar.deps controller/jar.deps satellite/jar.deps jclcrypto/jar.deps FORCE=1 VERSION=${VERSION}
RUN make .filelist FORCE=1 VERSION=${VERSION} PRESERVE_DEBIAN=1
# Don't remove caches - we need them for offline build
RUN rm -Rf .gradlehome/wrapper .gradlehome/native .gradlehome/.tmp || true
RUN mkdir -p ./libs
RUN make tgz VERSION=${VERSION}

# Extract tarball and build DEB packages from it
RUN mv linstor-server-${VERSION}.tar.gz /linstor-server_${VERSION}.orig.tar.gz \
 && tar -C / -xvf /linstor-server_${VERSION}.orig.tar.gz

WORKDIR /linstor-server-${VERSION}
# Verify .gradlehome is present in extracted tarball
RUN test -d .gradlehome && echo ".gradlehome found in tarball" || (echo ".gradlehome not found in tarball!" && exit 1)

# Build DEB packages from tarball
# Override GRADLE_FLAGS to remove --offline flag, allowing Gradle to download missing dependencies
RUN sed -i 's/GRADLE_FLAGS = --offline/GRADLE_FLAGS =/' debian/rules || true
RUN LD_LIBRARY_PATH='' dpkg-buildpackage -rfakeroot -b -uc

# Copy built .deb packages to a location accessible from final image
# dpkg-buildpackage creates packages in parent directory
RUN mkdir -p /packages-output && \
    find .. -maxdepth 1 -name "linstor-*.deb" -exec cp {} /packages-output/ \; && \
    test -n "$(ls -A /packages-output)" || (echo "ERROR: No linstor .deb packages found after build." && exit 1)

# ------------------------------------------------------------------------------
# Final image
FROM debian:${DISTRO}

LABEL maintainer="Roland Kammerer <roland.kammerer@linbit.com>"

ARG LINSTOR_VERSION
ARG DISTRO

# Copy built .deb packages from builder stage
# dpkg-buildpackage creates packages in parent directory, we copied them to /packages-output
COPY --from=builder /packages-output/ /packages/

RUN { echo 'APT::Install-Recommends "false";' ; echo 'APT::Install-Suggests "false";' ; } > /etc/apt/apt.conf.d/99_piraeus

RUN --mount=type=cache,target=/var/cache,sharing=private \
    --mount=type=cache,target=/var/lib/apt/lists,sharing=private \
    --mount=type=tmpfs,target=/var/log \
  # Install wget first for downloading keyring
   apt-get update && apt-get install -y wget ca-certificates && \
	# Enable contrib repos for zfsutils \
	 . /etc/os-release && \
	 sed -i -r 's/^Components: (.*)$/Components: \1 contrib/' /etc/apt/sources.list.d/debian.sources && \
	 echo "deb http://deb.debian.org/debian $VERSION_CODENAME-backports contrib" > /etc/apt/sources.list.d/backports.list && \
	 wget https://packages.linbit.com/public/linbit-keyring.deb -O /var/cache/linbit-keyring.deb && \
	 dpkg -i /var/cache/linbit-keyring.deb && \
	 echo "deb http://packages.linbit.com/public $VERSION_CODENAME misc" > /etc/apt/sources.list.d/linbit.list && \
	 apt-get update && \
	# Install useful utilities and general dependencies
	 apt-get install -y udev drbd-utils jq net-tools iputils-ping iproute2 dnsutils netcat-traditional sysstat curl util-linux && \
	# Install dependencies for optional features \
	 apt-get install -y \
	# cryptsetup: luks layer
	  cryptsetup \
	# e2fsprogs: LINSTOR can create file systems \
	  e2fsprogs \
	# lsscsi: exos layer \
	  lsscsi \
	# lvm2: manage lvm storage pools \
	  lvm2 \
	# multipath-tools: exos layer \
	  multipath-tools \
	# nvme-cli: nvme layer
	  nvme-cli \
	# procps: used by LINSTOR to find orphaned send/receive processes \
	  procps \
	# socat: used with thin-send-recv to send snapshots to another LINSTOR cluster
	  socat \
	# thin-send-recv: used to send/receive snapshots of LVM thin volumes \
	  thin-send-recv \
	# xfsprogs: LINSTOR can create file systems; xfs deps \
	  xfsprogs \
	# zstd: used with thin-send-recv to send snapshots to another LINSTOR cluster \
	  zstd \
	# zfsutils-linux: for zfs storage pools \
	  zfsutils-linux/$VERSION_CODENAME-backports \
	 && \
	# remove udev, no need for it in the container \
	 apt-get remove -y udev && \
	# Install linstor packages from built .deb files and linstor-client from repository
	 apt-get install -y default-jre-headless python3-all python3-natsort linstor-client \
	 && ls packages/*.deb >/dev/null && (dpkg -i packages/*.deb || apt-get install -f -y) \
	 && rm -rf /packages \
	 && sed -i 's/"-Djdk.tls.acknowledgeCloseNotify=true"//g' /usr/share/linstor-server/bin/Controller \
	 && apt-get clean

# Log directory need to be group writable. OpenShift assigns random UID and GID, without extra RBAC changes we can only influence the GID.
RUN mkdir /var/log/linstor-controller && \
	 chown 0:1000 /var/log/linstor-controller && \
	 chmod -R 0775 /var/log/linstor-controller && \
	 # Ensure we log to files in containers, otherwise SOS reports won't show any logs at all
	 sed -i 's#<!-- <appender-ref ref="FILE" /> -->#<appender-ref ref="FILE" />#' /usr/share/linstor-server/lib/conf/logback.xml


RUN lvmconfig --type current --mergedconfig --config 'activation { udev_sync = 0 udev_rules = 0 monitoring = 0 } devices { global_filter = [ "r|^/dev/drbd|" ] obtain_device_list_from_udev = 0}' > /etc/lvm/lvm.conf.new && mv /etc/lvm/lvm.conf.new /etc/lvm/lvm.conf
RUN echo 'global { usage-count no; }' > /etc/drbd.d/global_common.conf

# controller
EXPOSE 3376/tcp 3377/tcp 3370/tcp 3371/tcp

# satellite
EXPOSE 3366/tcp 3367/tcp

RUN wget https://raw.githubusercontent.com/piraeusdatastore/piraeus/refs/heads/master/dockerfiles/piraeus-server/entry.sh -O /usr/bin/piraeus-entry.sh \
 && chmod +x /usr/bin/piraeus-entry.sh

ARG K8S_AWAIT_ELECTION_VERSION=v0.4.2
# TARGETARCH is a docker special variable: https://docs.docker.com/engine/reference/builder/#automatic-platform-args-in-the-global-scope
ARG TARGETARCH

RUN wget https://github.com/LINBIT/k8s-await-election/releases/download/${K8S_AWAIT_ELECTION_VERSION}/k8s-await-election-${K8S_AWAIT_ELECTION_VERSION}-linux-${TARGETARCH}.tar.gz -O - | tar -xvz -C /usr/bin/

ARG LOSETUP_CONTAINER_VERSION=v1.0.1
RUN wget "https://github.com/LINBIT/losetup-container/releases/download/${LOSETUP_CONTAINER_VERSION}/losetup-container-$(uname -m)-unknown-linux-gnu.tar.gz" -O - | tar -xvz -C /usr/local/sbin && \
	 printf '#!/bin/sh\nLOSETUP_CONTAINER_ORIGINAL_LOSETUP=%s exec /usr/local/sbin/losetup-container "$@"\n' $(command -v losetup) > /usr/local/sbin/losetup && \
	 chmod +x /usr/local/sbin/losetup

RUN wget "https://dl.k8s.io/$(wget -O - https://dl.k8s.io/release/stable.txt)/bin/linux/${TARGETARCH}/kubectl" -O /usr/local/bin/kubectl && chmod +x /usr/local/bin/kubectl

CMD ["startSatellite"]
ENTRYPOINT ["/usr/bin/k8s-await-election", "/usr/bin/piraeus-entry.sh"]
