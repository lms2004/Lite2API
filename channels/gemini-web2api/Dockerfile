FROM python:3.12-slim@sha256:dd29372629eeba2dd003fd9e9d35a5b8236c44727875a0364254b5127af88e65

RUN groupadd --gid 10001 gemini && useradd --uid 10001 --gid 10001 --no-create-home --shell /usr/sbin/nologin gemini
WORKDIR /app
COPY --chown=10001:10001 third_party/gemini-web2api/gemini_web2api/ ./gemini_web2api/

USER 10001:10001
EXPOSE 45681
CMD ["python", "-m", "gemini_web2api", "--config", "/app/config.json"]
