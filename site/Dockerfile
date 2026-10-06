# syntax=docker/dockerfile:1.7

# The documentation site: build from this directory, e.g.
#   docker build -t tide-site site
FROM node:22.17.0-alpine3.22 AS build
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM nginxinc/nginx-unprivileged:1.29.8-alpine@sha256:0c79d56aee561a1d81c63f00eee5fb5fe29279560cdc55e91425133104c7fbe6
COPY nginx.conf /etc/nginx/nginx.conf
COPY --from=build /src/build /usr/share/nginx/html
EXPOSE 8080
