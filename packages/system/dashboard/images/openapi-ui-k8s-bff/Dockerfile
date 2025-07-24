# imported from https://github.com/PRO-Robotech/openapi-ui-k8s-bff/blob/main/Dockerfile.build
ARG NODE_VERSION=20.18.1
FROM node:${NODE_VERSION}-alpine AS builder
RUN apk add git
WORKDIR /src

ARG COMMIT_REF=003e7195245b81aad2b7be8c1111fdf019c22de3
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
