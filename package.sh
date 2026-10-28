#!/bin/sh
set -e
cd "$(dirname "$0")"

# compile for version
make
if [ $? -ne 0 ]; then
    echo "make error"
    exit 1
fi

lumo_version=`./bin/lumos --version`
echo "build version: $lumo_version"

# cross_compiles
make -f ./Makefile.cross-compiles

rm -rf ./release/packages
mkdir -p ./release/packages

os_all='linux windows darwin freebsd openbsd android'
arch_all='386 amd64 arm arm64 mips64 mips64le mips mipsle riscv64 loong64'
extra_all='_ hf'

cd ./release

for os in $os_all; do
    for arch in $arch_all; do
        for extra in $extra_all; do
            suffix="${os}_${arch}"
            if [ "x${extra}" != x"_" ]; then
                suffix="${os}_${arch}_${extra}"
            fi
            lumo_dir_name="lumo_${lumo_version}_${suffix}"
            lumo_path="./packages/lumo_${lumo_version}_${suffix}"

            if [ "x${os}" = x"windows" ]; then
                if [ ! -f "./lumoc_${suffix}.exe" ]; then
                    continue
                fi
                if [ ! -f "./lumos_${suffix}.exe" ]; then
                    continue
                fi
                mkdir ${lumo_path}
                mv ./lumoc_${suffix}.exe ${lumo_path}/lumoc.exe
                mv ./lumos_${suffix}.exe ${lumo_path}/lumos.exe
            else
                if [ ! -f "./lumoc_${suffix}" ]; then
                    continue
                fi
                if [ ! -f "./lumos_${suffix}" ]; then
                    continue
                fi
                mkdir ${lumo_path}
                mv ./lumoc_${suffix} ${lumo_path}/lumoc
                mv ./lumos_${suffix} ${lumo_path}/lumos
            fi
            cp ../LICENSE ${lumo_path}
            cp -f ../conf/lumos.toml ${lumo_path}
            cp -f ../conf/lumoc.toml ${lumo_path}

            # packages
            cd ./packages
            if [ "x${os}" = x"windows" ]; then
                if command -v zip >/dev/null 2>&1; then
                    zip -rq ${lumo_dir_name}.zip ${lumo_dir_name}
                else
                    /c/Windows/System32/tar.exe -a -c -f ${lumo_dir_name}.zip ${lumo_dir_name}
                fi
            else
                tar -zcf ${lumo_dir_name}.tar.gz ${lumo_dir_name}
            fi
            cd ..
            rm -rf ${lumo_path}
        done
    done
done

cd -
