{{if .Node.Standalone}}# Build the site, then ship only Next's standalone output on a small Node image.
{{else}}# Build the site, then ship it with its production dependencies on a small Node image.
{{end}}FROM node:24-alpine AS build
WORKDIR /src
{{if .Node.Corepack}}RUN corepack enable
{{end}}COPY {{.Node.Manifests}} ./
RUN {{.Node.Install}}
COPY . .
ENV NEXT_TELEMETRY_DISABLED=1
RUN {{.Node.Run}} build

FROM node:24-alpine
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 HOSTNAME=0.0.0.0 PORT={{.Port}}
WORKDIR /app
{{if .Node.Standalone}}COPY --from=build --chown=node:node /src/.next/standalone ./
COPY --from=build --chown=node:node /src/.next/static ./.next/static
{{if .Node.HasPublic}}COPY --from=build --chown=node:node /src/public ./public
{{end}}USER node
EXPOSE {{.Port}}
CMD ["node", "server.js"]
{{else}}{{if .Node.Corepack}}RUN corepack enable
{{end}}COPY {{.Node.Manifests}} ./
RUN {{.Node.InstallProd}}
COPY --from=build --chown=node:node /src/.next ./.next
{{if .Node.HasPublic}}COPY --from=build /src/public ./public
{{end}}{{if .Node.NextConfig}}COPY --from=build /src/{{.Node.NextConfig}} ./
{{end}}USER node
EXPOSE {{.Port}}
CMD ["node_modules/.bin/next", "start"]
{{end}}
