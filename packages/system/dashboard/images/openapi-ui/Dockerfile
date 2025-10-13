ARG NODE_VERSION=20.18.1

# openapi-k8s-toolkit
# imported from https://github.com/cozystack/openapi-k8s-toolkit
FROM node:${NODE_VERSION}-alpine AS openapi-k8s-toolkit-builder
WORKDIR /src
ARG COMMIT=4f57ab295b2a886eb294b0b987554194fbe67dcd
RUN wget -O- https://github.com/cozystack/openapi-k8s-toolkit/archive/${COMMIT}.tar.gz | tar -xzvf- --strip-components=1
RUN npm install
RUN npm install --build-from-source @swc/core
RUN npm run build


# openapi-ui
# imported from https://github.com/cozystack/openapi-ui
FROM node:${NODE_VERSION}-alpine AS builder
RUN apk add git
WORKDIR /src

ARG COMMIT_REF=65e7fa8b3dc530a36e94c8435622bb09961aef97
RUN wget -O- https://github.com/PRO-Robotech/openapi-ui/archive/${COMMIT_REF}.tar.gz | tar xzf - --strip-components=1

COPY patches /patches
RUN git apply /patches/*.diff

ENV PATH=/src/node_modules/.bin:$PATH

RUN npm install

# add patched openapi-k8s-toolkit
RUN rm -rf node_modules/@prorobotech/openapi-k8s-toolkit/dist
COPY --from=openapi-k8s-toolkit-builder /src/dist node_modules/@prorobotech/openapi-k8s-toolkit/dist

RUN npm run build

FROM node:${NODE_VERSION}-alpine AS builder2
WORKDIR /src
ENV PATH=/src/node_modules/.bin:$PATH

COPY --from=builder /src/server/package.json ./
COPY --from=builder /src/server/package-lock.json ./
RUN npm install
COPY --from=builder /src/server server
COPY --from=builder /src/tsconfig.server.json ./
COPY --from=builder /src/build /src/build
RUN npm run server:build

FROM node:${NODE_VERSION}-alpine
WORKDIR /app
COPY --from=builder2 /src/node_modules /app/node_modules
COPY --from=builder2 /src/build /app/build
EXPOSE 8080
RUN sed -i -e 's|OpenAPI UI|Cozystack|g' build/index.html
USER 1001
CMD ["node", "/app/build/index.js"]
