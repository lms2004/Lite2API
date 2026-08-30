FROM python:3.12-slim@sha256:09f7da3bc104798d0afb40bc08d23ab2da20a76130cec1f2ef170848f5d85217

RUN groupadd --gid 10001 gemini && useradd --uid 10001 --gid 10001 --no-create-home --shell /usr/sbin/nologin gemini
WORKDIR /app
COPY --chown=10001:10001 third_party/gemini-web2api/gemini_web2api/ ./gemini_web2api/

USER 10001:10001
EXPOSE 45681
CMD ["python", "-m", "gemini_web2api", "--config", "/app/config.json"]
