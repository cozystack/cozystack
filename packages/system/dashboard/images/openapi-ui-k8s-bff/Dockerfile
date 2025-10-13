# imported from https://github.com/cozystack/openapi-ui-k8s-bff
ARG NODE_VERSION=20.18.1
FROM node:${NODE_VERSION}-alpine AS builder
RUN apk add git
WORKDIR /src

ARG COMMIT_REF=22f9143f5109fb90332651c857d70b51bffccd9b
RUN wget -O- https://github.com/PRO-Robotech/openapi-ui-k8s-bff/archive/${COMMIT_REF}.tar.gz | tar xzf - --strip-components=1

COPY patches /patches
RUN git apply /patches/*.diff

ENV PATH=/src/node_modules/.bin:$PATH
RUN npm install
RUN npm run build

FROM node:${NODE_VERSION}-alpine
WORKDIR /app
COPY --from=builder /src/package*.json /app/
COPY --from=builder /src/node_modules /app/node_modules
COPY --from=builder /src/src/swagger/swagger-output.json /app/dist/swagger/swagger-output.json
COPY --from=builder /src/dist /app/dist
EXPOSE 8080
USER 1001
CMD [ "node", "/app/dist/index.js"]
