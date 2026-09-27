# Build the site, then ship only the Nitro output on a small Node image.
FROM node:24-alpine AS build
WORKDIR /src
{{if .Node.Corepack}}RUN corepack enable
{{end}}COPY {{.Node.Manifests}} ./
RUN {{.Node.Install}}
COPY . .
RUN {{.Node.Run}} build

FROM node:24-alpine
ENV NODE_ENV=production HOST=0.0.0.0 PORT={{.Port}}
WORKDIR /app
COPY --from=build /src/.output ./.output
USER node
EXPOSE {{.Port}}
CMD ["node", ".output/server/index.mjs"]
