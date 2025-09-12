# imported from https://github.com/PRO-Robotech/openapi-ui/blob/main/Dockerfile.build
ARG NODE_VERSION=20.18.1
FROM node:${NODE_VERSION}-alpine AS builder
RUN apk add git
WORKDIR /src

ARG COMMIT_REF=e91cc96468f9a9098f154eea3c9476ce0bc0756f
RUN wget -O- https://github.com/PRO-Robotech/openapi-ui/archive/${COMMIT_REF}.tar.gz | tar xzf - --strip-components=1

COPY patches /patches
RUN git apply /patches/*.diff

ENV PATH=/src/node_modules/.bin:$PATH
RUN npm install
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
USER 1001
CMD ["node", "/app/build/index.js"]
