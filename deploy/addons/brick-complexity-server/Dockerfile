FROM python:3.12-slim

WORKDIR /app

# System dependencies. apt-get upgrade patches OS packages that have fixes
# available at build time (reduces Trivy HIGH findings on the base image).
# Note: 2 perl-base CRITICALs (CVE-2026-42496, CVE-2026-8376) have no upstream
# fix yet and cannot be resolved here; they are tracked/monitored.
RUN apt-get update && apt-get upgrade -y \
    && apt-get install -y --no-install-recommends \
    curl \
    && rm -rf /var/lib/apt/lists/*

# Python dependencies (CPU-only PyTorch to keep image small)
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt \
    && rm -rf /root/.cache/pip

# Download models at build time for faster startup
RUN python3 -c "\
from transformers import AutoModelForCausalLM, AutoTokenizer; \
from peft import PeftModel; \
print('Downloading base model...'); \
AutoTokenizer.from_pretrained('Qwen/Qwen3.5-0.8B', trust_remote_code=True); \
base = AutoModelForCausalLM.from_pretrained('Qwen/Qwen3.5-0.8B', trust_remote_code=True); \
print('Downloading LoRA adapter...'); \
PeftModel.from_pretrained(base, 'regolo/brick-complexity-2-eco'); \
print('Models cached.'); \
"

COPY server.py .

EXPOSE 8093

HEALTHCHECK --interval=30s --timeout=10s --retries=3 \
    CMD curl -f http://localhost:8093/health || exit 1

ENTRYPOINT ["python3", "server.py"]
CMD ["--port", "8093", "--device", "auto"]
