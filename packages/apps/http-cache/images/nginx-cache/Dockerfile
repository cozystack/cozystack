FROM ubuntu:22.04 AS stage

ARG NGINX_VERSION=1.25.3
ARG IP2LOCATION_C_VERSION=8.6.1
ARG IP2PROXY_C_VERSION=4.1.2
ARG IP2LOCATION_NGINX_VERSION=8.6.0
ARG IP2PROXY_NGINX_VERSION=8.1.1
ARG FIFTYONEDEGREES_NGINX_VERSION=3.2.21.1
ARG NGINX_CACHE_PURGE_VERSION=2.5.3
ARG NGINX_VTS_VERSION=0.2.2

ARG TARGETOS
ARG TARGETARCH

# Install required packages for development
RUN apt update -q \
 && apt install -yq --no-install-recommends \
    ca-certificates \
    unzip \
    automake \
    build-essential \
    libtool \
    libpcre3 \
    libpcre3-dev \
    libssl-dev \
    libgd-dev \
    zlib1g-dev \
    gcc \
    make \
    git \
    wget \
    curl \
    checkinstall

# Download sources
RUN mkdir ip2location \
 && curl -sS -L https://github.com/chrislim2888/IP2Location-C-Library/archive/refs/tags/${IP2LOCATION_C_VERSION}.tar.gz \
 | tar -C ip2location -xzvf- --strip=1

RUN mkdir ip2proxy \
 && curl -sS -L https://github.com/ip2location/ip2proxy-c/archive/refs/tags/${IP2PROXY_C_VERSION}.tar.gz \
 | tar -C ip2proxy -xzvf- --strip=1

RUN mkdir ip2mod-location \
 && curl -sS -L https://github.com/ip2location/ip2location-nginx/archive/refs/tags/${IP2LOCATION_NGINX_VERSION}.tar.gz \
 | tar -C ip2mod-location -xzvf- --strip=1

RUN mkdir ip2mod-proxy \
 && curl -sS -L https://github.com/ip2location/ip2proxy-nginx/archive/refs/tags/${IP2PROXY_NGINX_VERSION}.tar.gz \
 | tar -C ip2mod-proxy -xzvf- --strip=1

RUN mkdir cache-purge-module \
 && curl -sS -L https://github.com/nginx-modules/ngx_cache_purge/archive/refs/tags/${NGINX_CACHE_PURGE_VERSION}.tar.gz \
 | tar -C cache-purge-module -xzvf- --strip=1

RUN mkdir nginx-module-vts \
 && curl -sS -L https://github.com/vozlt/nginx-module-vts/archive/refs/tags/v${NGINX_VTS_VERSION}.tar.gz \
 | tar -C nginx-module-vts -xzvf- --strip=1

RUN mkdir nginx \
 && curl -sS -L https://nginx.org/download/nginx-${NGINX_VERSION}.tar.gz \
 | tar -C nginx -xzvf- --strip=1

# Compile C Library for IP2Location module
WORKDIR /ip2location
RUN autoreconf -i -v --force
RUN ./configure
RUN make
RUN checkinstall \
  -D \
  --install=no \
  --default \
  --pkgname=ip2location-c \
  --pkgversion=${IP2LOCATION_C_VERSION} \
  --pkgarch=${TARGETARCH} \
  --pkggroup=lib \
  --pkgsource="https://github.com/chrislim2888/IP2Location-C-Library" \
  --maintainer="Eduard Generalov <eduard@generalov.net>" \
  --requires=librtmp1 \
  --autodoinst=no \
  --deldoc=yes \
  --deldesc=yes \
  --delspec=yes \
  --backup=no \
  make install

WORKDIR /ip2location/data
RUN perl ip-country.pl

WORKDIR /ip2location/test
RUN ./test-IP2Location

# Compile C Library for IP2Proxy module
WORKDIR /ip2proxy
RUN autoreconf -i -v --force
RUN ./configure
RUN make
RUN checkinstall \
  -D \
  --install=no \
  --default \
  --pkgname=ip2proxy-c \
  --pkgversion=${IP2PROXY_C_VERSION} \
  --pkgarch=${TARGETARCH} \
  --pkggroup=lib \
  --pkgsource="https://github.com/ip2location/ip2proxy-c" \
  --maintainer="Eduard Generalov <eduard@generalov.net>" \
  --requires=librtmp1 \
  --autodoinst=no \
  --deldoc=yes \
  --deldesc=yes \
  --delspec=yes \
  --backup=no \
  make install

# Compile Nginx
WORKDIR /nginx
RUN ./configure \
  --with-compat \
  --prefix=/usr/share/nginx \
  --sbin-path=/usr/bin/nginx \
  --with-http_ssl_module \
  --conf-path=/etc/nginx/nginx.conf \
  --http-log-path=/var/log/nginx/access.log \
  --error-log-path=/var/log/nginx/error.log \
  --lock-path=/var/lock/nginx.lock \
  --pid-path=/run/nginx.pid \
  --modules-path=/usr/lib/nginx/modules \
  --http-client-body-temp-path=/var/lib/nginx/body \
  --http-fastcgi-temp-path=/var/lib/nginx/fastcgi \
  --http-proxy-temp-path=/var/lib/nginx/proxy \
  --http-scgi-temp-path=/var/lib/nginx/scgi \
  --http-uwsgi-temp-path=/var/lib/nginx/uwsgi \
  --with-http_realip_module \
  --with-http_stub_status_module \
  --with-stream \
  --add-module=../nginx-module-vts \
  --add-module=../cache-purge-module \
  --add-dynamic-module=../ip2mod-proxy \
  --add-dynamic-module=../ip2mod-location \
  --with-compat

RUN make modules
RUN make -j 8
RUN checkinstall \
  -D \
  --install=no \
  --default \
  --pkgname=nginx \
  --pkgversion=$VERS \
  --pkgarch=${TARGETARCH} \
  --pkggroup=web \
  --provides=nginx \
  --requires=ip2location-c,ip2proxy-c,libssl3,libc-bin,libc6,libzstd1,libpcre++0v5,libpcre16-3,libpcre2-8-0,libpcre3,libpcre32-3,libpcrecpp0v5,libmaxminddb0 \
  --autodoinst=no \
  --deldoc=yes \
  --deldesc=yes \
  --delspec=yes \
  --backup=no \
  make install

RUN mkdir /packages \
 && mv /*/*.deb /packages/

FROM ubuntu:22.04
COPY --from=stage /packages /packages
COPY nginx-reloader.sh /usr/bin/nginx-reloader.sh

RUN set -x \
 && groupadd --system --gid 101 nginx \
 && useradd --system --gid nginx --no-create-home --home /nonexistent --comment "nginx user" --shell /bin/false --uid 101 nginx \
 && apt update -q \
 && apt install -yq --no-install-recommends --no-install-suggests gnupg1 ca-certificates inotify-tools \
 && apt install -y /packages/*.deb \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /var/lib/nginx /var/log/nginx \
 && ln -sf /dev/stdout /var/log/nginx/access.log \
 && ln -sf /dev/stderr /var/log/nginx/error.log \
 && chown -R nginx: /var/lib/nginx /var/log/nginx

ENTRYPOINT ["/usr/bin/nginx", "-g", "daemon off;"]
