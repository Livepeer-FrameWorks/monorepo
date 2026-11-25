# The toolchain of the CI "ansible-lint + yamllint" job (.github/workflows/ansible.yml):
# the same Python and the same requirements file. make ansible-lint-image builds it,
# tagged with a hash of this file and requirements-ci-lint.txt.
FROM python:3.12.16-slim@sha256:5e2bdb1b0d542d5039cffb8181c65930093949194a65c365dfb1b0fe13a4fa5b
COPY requirements-ci-lint.txt /tmp/requirements-ci-lint.txt
RUN python -m pip install --no-cache-dir --upgrade pip \
	&& pip install --no-cache-dir -r /tmp/requirements-ci-lint.txt
