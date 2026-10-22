# syntax=docker/dockerfile:1.7

# The documentation site: build from this directory, e.g.
#   docker build -t tide-site site
FROM node:26.3.0-alpine3.22 AS build
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM nginxinc/nginx-unprivileged:1.31.7-alpine@sha256:b5047b9b76845eac124358373c6852377c52e4b05834a47647e955dd91cb8f1e
COPY nginx.conf /etc/nginx/nginx.conf
COPY --from=build /src/build /usr/share/nginx/html
EXPOSE 8080
