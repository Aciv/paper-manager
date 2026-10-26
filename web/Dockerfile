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
ARG VERSION=1.0.0
ARG VCS_REF=unknown
LABEL org.opencontainers.image.title="paper_manager web" \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$VCS_REF \
      org.opencontainers.image.source="https://github.com/Aciv/paper-manager"
COPY web/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /web/dist /usr/share/nginx/html
COPY --from=build /web/node_modules/@fontsource-variable/manrope/LICENSE /licenses/Manrope-OFL.txt
COPY --from=build /web/node_modules/@fontsource-variable/noto-sans-sc/LICENSE /licenses/NotoSansSC-OFL.txt
COPY LICENSE /licenses/paper_manager-MIT.txt
EXPOSE 80
