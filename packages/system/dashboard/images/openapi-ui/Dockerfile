ARG NODE_VERSION=20.18.1

# openapi-k8s-toolkit
# imported from https://github.com/PRO-Robotech/openapi-k8s-toolkit
FROM node:${NODE_VERSION}-alpine AS openapi-k8s-toolkit-builder
RUN apk add git
WORKDIR /src
# release/1.4.0
ARG COMMIT=c67029cc7b7495c65ee0406033576e773a73bb01
RUN wget -O- https://github.com/PRO-Robotech/openapi-k8s-toolkit/archive/${COMMIT}.tar.gz | tar -xzvf- --strip-components=1

COPY openapi-k8s-toolkit/patches /patches
RUN git apply /patches/*.diff

RUN npm install
RUN npm install --build-from-source @swc/core
RUN npm run build


# openapi-ui
# imported from https://github.com/PRO-Robotech/openapi-ui
FROM node:${NODE_VERSION}-alpine AS builder
#RUN apk add git
WORKDIR /src

# release/1.4.0
ARG COMMIT_REF=6addca6939264ef2e39801baa88c1460cc1aa53e
RUN wget -O- https://github.com/PRO-Robotech/openapi-ui/archive/${COMMIT_REF}.tar.gz | tar xzf - --strip-components=1

#COPY openapi-ui/patches /patches
#RUN git apply /patches/*.diff

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
# Fix Factory component: return null while loading instead of showing "Factory Not Found" 404
RUN APP_JS=$(find build -name "App-react.js" -type f | head -1) && \
    if [ -n "$APP_JS" ]; then \
      sed -i 's|const { data: factoryData } = useK8sSmartResource({|const { data: factoryData, isLoading: factoryIsLoading } = useK8sSmartResource({|' "$APP_JS" && \
      sed -i '/Factory Not Found/s/return /return factoryIsLoading ? null : /' "$APP_JS" && \
      echo "Factory loading patch applied to $APP_JS"; \
    fi
USER 1001
CMD ["node", "/app/build/index.js"]
