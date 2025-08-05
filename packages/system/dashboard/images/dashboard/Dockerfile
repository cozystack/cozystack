FROM bitnami/node:20.15.1 AS build
WORKDIR /app

ARG COMMIT_REF=4926bc68fabb0914afab574006643c85a597b371
RUN wget -O- https://github.com/cozystack/kubeapps/archive/${COMMIT_REF}.tar.gz | tar xzf - --strip-components=2 kubeapps-${COMMIT_REF}/dashboard

RUN yarn install --frozen-lockfile

RUN yarn run prettier-check && yarn run ts-compile-check
RUN yarn run build

RUN sed -i \
    -e 's/#2d4048/#202124/g' \
    -e 's/#25333d/#1e2023/g'  \
    -e 's/#fcfdfd/#f3f4f5/g' \
    -e 's/#f1f6f8/#e7e9eb/g' \
    -e 's/#e3eaed/#d3d6da/g' \
    -e 's/#cbd4d8/#b7bbc1/g' \
    -e 's/#aeb8bc/#989da3/g' \
    -e 's/#859399/#7b7f85/g' \
    -e 's/#6a7a81/#5b686e/g' \
    -e 's/#4f6169/#4f5256/g' \
    -e 's/#3a4d55/#3a3d41/g' \
    -e 's/#2d4048/#202124/g' \
    -e 's/#21333b/#383d44/g' \
    -e 's/#1b2b32/#2a2d2f/g' \
    $(grep -rl "#2d4048\|#25333d\|#fcfdfd\|#f1f6f8\|#e3eaed\|#cbd4d8\|#aeb8bc\|#859399\|#6a7a81\|#4f6169\|#3a4d55\|#2d4048\|#21333b\|#1b2b32")

FROM bitnami/nginx:1.25.2
COPY --from=build /app/build /app
