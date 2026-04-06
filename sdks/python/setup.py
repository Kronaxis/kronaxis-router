from setuptools import setup, find_packages

setup(
    name="kronaxis-router",
    version="0.1.0",
    description="Python SDK for Kronaxis Router - intelligent LLM proxy",
    long_description=open("README.md").read(),
    long_description_content_type="text/markdown",
    author="Kronaxis",
    author_email="dev@kronaxis.co.uk",
    url="https://github.com/kronaxis/kronaxis-router",
    packages=find_packages(),
    python_requires=">=3.8",
    install_requires=[],  # Zero dependencies -- uses stdlib urllib
    classifiers=[
        "Development Status :: 4 - Beta",
        "Intended Audience :: Developers",
        "License :: OSI Approved :: Apache Software License",
        "Programming Language :: Python :: 3",
        "Topic :: Software Development :: Libraries",
    ],
)
