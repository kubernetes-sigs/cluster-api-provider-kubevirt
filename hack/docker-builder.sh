#!/bin/bash

set -e

DOCKER_BUILD_IMAGE=${DOCKER_BUILD_IMAGE:-"golang:1.17"}
DOCKER_BUILD_VOLUME=${DOCKER_BUILD_VOLUME:-"capk_build_cache"}
DOCKER_BUILD_CACHE=${DOCKER_BUILD_CACHE:-"/root/.cache/go-build"}

BIN_DIR=${BIN_DIR:-"bin"}

CGO_ENABLED=0
GOOS=linux
GOARCH=amd64

TMP_BUILD_SCRIPT=".docker-build-script.sh"

mkdir -p $BIN_DIR


create_tmp_build_script() {

	_uid=$(id -u)
	_gid=$(id -g)

	cat << EOF > ${TMP_BUILD_SCRIPT}
#!/bin/bash
set -e
go build -o "${BIN_DIR}/manager"
chown "${_uid}:${_gid}" "${BIN_DIR}/manager"
EOF
chmod 755 .docker-build-script.sh

}

if [ "${CAPK_DOCKERIZED_BUILD}" == "true" ]; then
	echo "Using dockerized builder"

	create_tmp_build_script
	docker volume create "$DOCKER_BUILD_VOLUME"
	docker run --rm \
		--mount type=bind,source="${PWD}",target="/src" \
		--mount source="${DOCKER_BUILD_VOLUME}",target="${DOCKER_BUILD_CACHE}" \
		-e GOMODCACHE="${DOCKER_BUILD_CACHE}" \
		-e CGO_ENABLED=${CGO_ENABLED} \
		-e GOOS=${GOOS} \
		-e GOARCH=${GOARCH} \
		--workdir "/src" \
		$DOCKER_BUILD_IMAGE \
		"./$TMP_BUILD_SCRIPT"

	rm ${TMP_BUILD_SCRIPT}
else 
	go build -o ${BIN_DIR}/manager
fi
