# ---- build ----
FROM node:20-alpine AS build

WORKDIR /app

ARG VITE_API_BASE=""
ENV VITE_API_BASE=${VITE_API_BASE}
# 国内构建可传 --build-arg NPM_REGISTRY=https://registry.npmmirror.com
ARG NPM_REGISTRY="https://registry.npmmirror.com"

COPY package.json ./
RUN if [ -n "$NPM_REGISTRY" ]; then npm config set registry "$NPM_REGISTRY"; fi && npm install

COPY . .
RUN npm run build

# ---- runtime: nginx 静态托管 + 同源反代 API / WebSocket ----
FROM nginx:alpine

COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html

EXPOSE 80
