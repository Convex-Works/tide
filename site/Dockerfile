# syntax=docker/dockerfile:1.7

# The documentation site: build from this directory, e.g.
#   docker build -t tide-site site
FROM node:22.17.0-alpine3.22 AS build
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM nginxinc/nginx-unprivileged:1.31.6-alpine@sha256:884e0b7f42f303641b0f4e78755aea647521dca0b89ca3be7992ad78b5c07434
COPY nginx.conf /etc/nginx/nginx.conf
COPY --from=build /src/build /usr/share/nginx/html
EXPOSE 8080
