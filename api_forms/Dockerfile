# syntax=docker/dockerfile:1

FROM node:24-alpine AS base

WORKDIR /app

# Install dependencies
COPY api_forms/package*.json ./
RUN npm ci --omit=dev

# Copy application code
COPY api_forms/. .

EXPOSE 18032

ENV NODE_ENV=production

CMD ["node", "server.js"]
