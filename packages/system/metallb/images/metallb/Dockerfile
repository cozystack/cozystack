# syntax=docker/dockerfile:1.2@sha256:e2a8561e419ab1ba6b2fe6cbdf49fd92b95912df1cf7d313c3e2230a333fdbcc

ARG VERSION

FROM quay.io/metallb/controller:${VERSION} as controller

FROM quay.io/metallb/speaker:${VERSION} as speaker
