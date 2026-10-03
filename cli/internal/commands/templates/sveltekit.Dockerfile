# Build the site, then ship adapter-node's output with the production
# dependencies on a small Node image.
FROM node:24-alpine AS build
WORKDIR /src
{{if .Node.Corepack}}RUN corepack enable
{{end}}COPY {{.Node.Manifests}} ./
RUN {{.Node.Install}}
COPY . .
RUN {{.Node.Run}} build
RUN {{.Node.Prune}}

FROM node:24-alpine
ENV NODE_ENV=production PORT={{.Port}}
WORKDIR /app
COPY --from=build /src/package.json ./
COPY --from=build /src/node_modules ./node_modules
COPY --from=build /src/build ./build
USER node
EXPOSE {{.Port}}
CMD ["node", "build"]
