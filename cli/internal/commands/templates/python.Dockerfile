# Install into a virtual environment, then copy only that onto the slim image.
FROM python:{{.Python.Version}}-slim AS build
WORKDIR /src
RUN python -m venv /opt/venv
ENV PATH=/opt/venv/bin:$PATH
{{if .Python.Requirements}}COPY requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt
{{else}}COPY . .
RUN pip install --no-cache-dir .
{{end}}
FROM python:{{.Python.Version}}-slim
ENV PATH=/opt/venv/bin:$PATH PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1
WORKDIR /app
COPY --from=build /opt/venv /opt/venv
COPY . .
RUN useradd --create-home app
USER app
EXPOSE {{.Port}}
CMD {{.Python.Command}}
