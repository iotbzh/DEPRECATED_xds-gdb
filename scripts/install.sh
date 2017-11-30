#!/bin/bash
###########################################################################
# Copyright 2017 IoT.bzh
#
# author: Sebastien Douheret <sebastien@iot.bzh>
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# Script used to install XDS gdb tool.
#
###########################################################################


DESTDIR=${DESTDIR:-/opt/AGL/xds/gdb}

ROOT_SRCDIR=$(cd $(dirname "$0")/.. && pwd)

install() {
    mkdir -p ${DESTDIR} && cp ${ROOT_SRCDIR}/bin/* ${DESTDIR} || exit 1

    FILE=/etc/profile.d/xds-gdb.sh
    sed -e "s;%%XDS_INSTALL_BIN_DIR%%;${DESTDIR};g" ${ROOT_SRCDIR}/conf.d/${FILE} > ${FILE} || exit 1
}

uninstall() {
    rm -rf "${DESTDIR}"
    rm -f /etc/profile.d/xds-gdb.sh
}

if [ "$1" == "uninstall" ]; then
    echo -n "Are-you sure you want to remove ${DESTDIR} [y/n]? "
    read answer
    if [ "${answer}" = "y" ]; then
        uninstall
        echo "xds-gdb sucessfully uninstalled."
    else
        echo "Uninstall canceled."
    fi
else
    install
fi
