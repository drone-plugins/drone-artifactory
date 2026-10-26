#!/bin/sh
set -eu

# Java-only root removal required by https://ops.java/emudhra/.
storepass=changeit
keystore=/etc/ssl/certs/java/cacerts
listing=$(keytool -J-Duser.language=en -list -v -keystore "$keystore" -storepass "$storepass")
printf '%s\n' "$listing" | awk '
    /^Alias name: / { alias = substr($0, 13) }
    /^[[:space:]]*SHA256:/ {
        fingerprint = $2
        gsub(/:/, "", fingerprint)
        if (fingerprint == "40F6AF0346A99AA1CD1D555A4E9CCE62C7F9634603EE406615833DC8C8D00367" ||
            fingerprint == "1AA0C2709E831BD6E3B5129A00BA41F7EEEF020872F1E6504BF0F6C3F24F3AF3" ||
            fingerprint == "86A1ECBA089C4A8D3BBE2734C612BA341D813E043CF9E8A862CD5C57A36BBE6B" ||
            fingerprint == "D7A7A0FB5D7E2731D771E9484EBCDEF71D5F0C3E0A2948782BC83EE0EA699EF4") print alias
    }
' | while IFS= read -r alias; do
    keytool -delete -keystore "$keystore" -storepass "$storepass" -alias "$alias"
done
