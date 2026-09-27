{{if .Node.HasBuild}}# Install and build with every dependency, then ship the result without the
# development ones, running as the unprivileged node user.
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
COPY --from=build --chown=node:node /src ./
{{else}}# Install the production dependencies only, and run as the unprivileged node user.
FROM node:24-alpine
ENV NODE_ENV=production PORT={{.Port}}
WORKDIR /app
{{if .Node.Corepack}}RUN corepack enable
{{end}}COPY {{.Node.Manifests}} ./
RUN {{.Node.InstallProd}}
COPY --chown=node:node . .
{{end}}USER node
EXPOSE {{.Port}}
CMD {{.Node.Start}}
