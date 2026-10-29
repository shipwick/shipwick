# syntax=docker/dockerfile:1

# Shipwick dashboard. Build context: this directory.
#
#   docker build -t ghcr.io/shipwick/dashboard dashboard/
#   docker run -p 127.0.0.1:3000:3000 -e SHIPWICK_AGENT_URL=http://agent:9000 ghcr.io/shipwick/dashboard

# ---- build -------------------------------------------------------------------
# Built on the builder's own architecture: .output is plain JavaScript (no
# native modules), so only the runtime stage differs per platform.
FROM --platform=$BUILDPLATFORM node:24-alpine AS build
WORKDIR /app

# Dependencies first, so this layer is cached until the lockfile changes.
# Yarn is the version package.json pins, fetched by corepack. --mode=skip-build
# skips this package's `nuxt prepare` postinstall (the sources are not copied
# yet, and `nuxt build` prepares anyway); the dependencies' own install scripts
# stay off, as Yarn 4 has them by default.
COPY package.json yarn.lock .yarnrc.yml ./
RUN corepack enable && yarn install --immutable --mode=skip-build

COPY . .
RUN yarn build

# ---- runtime -----------------------------------------------------------------
# .output is self-contained (Nitro bundles and traces its dependencies):
# no node_modules, no Yarn, no source in the final image.
FROM node:24-alpine AS runtime

ENV NODE_ENV=production \
    HOST=0.0.0.0 \
    PORT=3000 \
    SHIPWICK_AGENT_URL=http://127.0.0.1:9000

WORKDIR /app
COPY --from=build --chown=node:node /app/.output ./.output

# The official image ships an unprivileged `node` user (uid 1000).
USER node
EXPOSE 3000

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD node -e "fetch('http://127.0.0.1:'+(process.env.PORT||3000)+'/api/session').then(r=>process.exit(r.ok?0:1),()=>process.exit(1))"

CMD ["node", ".output/server/index.mjs"]
