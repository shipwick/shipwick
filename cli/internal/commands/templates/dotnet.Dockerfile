# Publish with the SDK, then run on the runtime image only, as the app user.
FROM mcr.microsoft.com/dotnet/sdk:{{.Dotnet.Version}} AS build
WORKDIR /src
COPY {{.Dotnet.Project}} ./
RUN dotnet restore
COPY . .
RUN dotnet publish -c Release -o /app --no-restore

FROM mcr.microsoft.com/dotnet/{{.Dotnet.Runtime}}:{{.Dotnet.Version}}
WORKDIR /app
COPY --from=build /app ./
{{if .Dotnet.Web}}ENV ASPNETCORE_URLS=http://0.0.0.0:{{.Port}}
{{end}}{{if not .Dotnet.AppUser}}RUN useradd --uid 1654 --user-group --no-create-home app
{{end}}USER app
{{if .Dotnet.Web}}EXPOSE {{.Port}}
{{end}}ENTRYPOINT ["dotnet", "{{.Dotnet.Assembly}}.dll"]
