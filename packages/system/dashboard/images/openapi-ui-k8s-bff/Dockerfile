# imported from https://github.com/cozystack/openapi-ui-k8s-bff
ARG NODE_VERSION=20.18.1
FROM node:${NODE_VERSION}-alpine AS builder
WORKDIR /src

ARG COMMIT_REF=92906a7f21050cfb8e352f98d36b209c57844f63
RUN wget -O- https://github.com/PRO-Robotech/openapi-ui-k8s-bff/archive/${COMMIT_REF}.tar.gz | tar xzf - --strip-components=1

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
