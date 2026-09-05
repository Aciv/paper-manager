FROM node:22-alpine AS build
ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG NO_PROXY
WORKDIR /web
COPY web/package.json web/package-lock.json ./
# TypeScript 7 distributes its platform executable as an optional package.
# Include optional dependencies explicitly for clean Linux container builds.
# As with the other service images, proxy values stay build-scoped.
RUN HTTP_PROXY="$HTTP_PROXY" HTTPS_PROXY="${HTTPS_PROXY:-$HTTP_PROXY}" NO_PROXY="$NO_PROXY" \
    http_proxy="$HTTP_PROXY" https_proxy="${HTTPS_PROXY:-$HTTP_PROXY}" no_proxy="$NO_PROXY" \
    npm ci --include=optional
COPY web .
ARG VITE_API_URL=/api/document
ENV VITE_API_URL=$VITE_API_URL
RUN npm run build

FROM nginx:1.27-alpine
COPY web/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /web/dist /usr/share/nginx/html
EXPOSE 80
