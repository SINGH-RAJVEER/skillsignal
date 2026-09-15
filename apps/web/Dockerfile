FROM docker.io/oven/bun:1.4 AS build

WORKDIR /src
COPY package.json bun.lock* ./
COPY tsconfig.base.json ./tsconfig.base.json
COPY apps/web/package.json ./apps/web/package.json
COPY packages/ui/package.json ./packages/ui/package.json
RUN bun install --frozen-lockfile
COPY apps/web ./apps/web
COPY packages/ui ./packages/ui
ARG VITE_API_URL=http://localhost:8000
ENV VITE_API_URL=$VITE_API_URL
RUN cd apps/web && bun run build

FROM docker.io/library/nginx:1.29-alpine
COPY apps/web/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /src/apps/web/dist /usr/share/nginx/html
EXPOSE 3000
