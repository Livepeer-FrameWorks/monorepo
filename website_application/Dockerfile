# Build stage
FROM node:18-alpine AS builder

WORKDIR /app

# Copy package files
COPY package*.json ./
RUN npm ci

# Copy source code
COPY . .

# Pass environment variables to build process
ARG VITE_API_URL
ARG VITE_ANALYTICS_API_URL
ARG VITE_INGEST_DOMAIN
ARG VITE_CDN_DOMAIN
ARG VITE_RTMP_PATH
ARG VITE_HLS_PATH
ARG VITE_EMBED_PATH
ARG VITE_MARKETING_SITE_URL

ENV VITE_API_URL=${VITE_API_URL}
ENV VITE_ANALYTICS_API_URL=${VITE_ANALYTICS_API_URL}
ENV VITE_INGEST_DOMAIN=${VITE_INGEST_DOMAIN}
ENV VITE_CDN_DOMAIN=${VITE_CDN_DOMAIN}
ENV VITE_RTMP_PATH=${VITE_RTMP_PATH}
ENV VITE_HLS_PATH=${VITE_HLS_PATH}
ENV VITE_EMBED_PATH=${VITE_EMBED_PATH}
ENV VITE_MARKETING_SITE_URL=${VITE_MARKETING_SITE_URL}

# Build the app
RUN npm run build

# Production stage
FROM node:18-alpine AS runner

WORKDIR /app

# Copy package files
COPY package*.json ./

# Install only production dependencies
RUN npm ci --only=production && npm cache clean --force

# Copy built app from builder stage
COPY --from=builder /app/build ./build
COPY --from=builder /app/static ./static

EXPOSE 18030

ENV NODE_ENV=production

CMD ["node", "build/index.js"] 