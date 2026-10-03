{{if .Python.Uv}}# Install exactly what uv.lock pins into a virtual environment next to the
# project, then copy both onto the slim image.
FROM python:{{.Python.Version}}-slim AS build
COPY --from=ghcr.io/astral-sh/uv:latest /uv /usr/local/bin/uv
ENV UV_COMPILE_BYTECODE=1 UV_LINK_MODE=copy UV_PYTHON_DOWNLOADS=never
WORKDIR /app
COPY pyproject.toml uv.lock ./
RUN uv sync --frozen --no-dev --no-install-project
COPY . .
RUN uv sync --frozen --no-dev

FROM python:{{.Python.Version}}-slim
ENV PATH=/app/.venv/bin:$PATH PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1
WORKDIR /app
COPY --from=build /app /app
{{else}}# Install into a virtual environment, then copy only that onto the slim image.
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
{{end}}RUN useradd --create-home app
USER app
EXPOSE {{.Port}}
CMD {{.Python.Command}}
